package main

import (
	"database/sql"
	"log"
	"sync"
)

type writeJob struct {
	docID int64
	anns  []Annotation
	force bool
}

var (
	writeCh     chan writeJob
	writeOnce   sync.Once
	writeWorker *sync.WaitGroup
)

// pending_writes 让写回作业跨重启：只有写回成功才删行，失败累加 attempts，超限才放弃。
const pendingWritesSchema = `
CREATE TABLE IF NOT EXISTS pending_writes (
    document_id INTEGER PRIMARY KEY,
    force INTEGER NOT NULL DEFAULT 0,
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);`

const maxWriteAttempts = 3

func persistWrite(db *sql.DB, docID int64, force bool) error {
	f := 0
	if force {
		f = 1
	}
	// force 只能升不能降：后到的普通提交不能把强制覆盖洗掉
	_, err := db.Exec(`INSERT INTO pending_writes (document_id, force) VALUES (?, ?)
		ON CONFLICT(document_id) DO UPDATE SET force=max(force, excluded.force)`, docID, f)
	return err
}

func clearPersistedWrite(db *sql.DB, docID int64) {
	db.Exec("DELETE FROM pending_writes WHERE document_id=?", docID)
}

// bumpWriteAttempt 失败后累加；达到上限才放弃，避免重启后无限重试。
func bumpWriteAttempt(db *sql.DB, docID int64) {
	db.Exec("UPDATE pending_writes SET attempts=attempts+1 WHERE document_id=?", docID)
	db.Exec("DELETE FROM pending_writes WHERE document_id=? AND attempts>=?", docID, maxWriteAttempts)
}

// loadPendingWrites 启动时把未完成作业重新排入内存队列。
func loadPendingWrites(db *sql.DB) []writeJob {
	rows, err := db.Query("SELECT document_id, force FROM pending_writes ORDER BY created_at, document_id")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var jobs []writeJob
	for rows.Next() {
		var id int64
		var force int
		if rows.Scan(&id, &force) != nil {
			continue
		}
		jobs = append(jobs, writeJob{docID: id, force: force == 1})
	}
	return jobs
}

// pendingWriteCount 供首页判断“写回是否真的完成”，避免入队就弹完成框。
func pendingWriteCount(db *sql.DB) int {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM pending_writes").Scan(&n); err != nil {
		return 0
	}
	return n
}

// startWriteWorker 启动单工写入队列：语雀和 SQLite 都不适合并发写。
func startWriteWorker(db *sql.DB, c Config) {
	writeOnce.Do(func() {
		db.Exec(pendingWritesSchema)
		writeCh = make(chan writeJob, 32)
		writeWorker = &sync.WaitGroup{}
		writeWorker.Add(1)
		go func() {
			defer writeWorker.Done()
			for job := range writeCh {
				warnings, err := writeBack(db, c, job.docID, job.anns, job.force)
				if err != nil {
					log.Printf("后台写回失败 doc=%d: %v", job.docID, err)
					markFailed(db, job.docID, err)
					bumpWriteAttempt(db, job.docID)
					continue
				}
				clearPersistedWrite(db, job.docID)
				log.Printf("后台写回完成 doc=%d warnings=%v", job.docID, warnings)
			}
		}()
		for _, job := range loadPendingWrites(db) {
			if anns, err := selectedTerms(db, job.docID); err == nil {
				job.anns = anns
			}
			if enqueueWrite(job) {
				log.Printf("恢复未完成的写回作业 doc=%d", job.docID)
			}
		}
	})
}

// resetWriteWorker 仅供测试：让每个用例拥有自己的队列和 DB。
func resetWriteWorker() {
	writeOnce = sync.Once{}
	writeCh = nil
}

func enqueueWrite(job writeJob) bool {
	select {
	case writeCh <- job:
		return true
	default:
		return false
	}
}
