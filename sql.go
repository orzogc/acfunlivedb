package main

import (
	"context"
	"database/sql"

	_ "modernc.org/sqlite"
)

// 新建table
const createTable = `CREATE TABLE IF NOT EXISTS acfunlive (
	liveID TEXT PRIMARY KEY,
	uid INTEGER NOT NULL,
	name TEXT NOT NULL,
	streamName TEXT NOT NULL UNIQUE,
	startTime INTEGER NOT NULL,
	title TEXT NOT NULL,
	duration INTEGER NOT NULL,
	playbackURL TEXT NOT NULL,
	backupURL TEXT NOT NULL,
	liveCutNum INTEGER NOT NULL UNIQUE
);
`

// 插入live
const insertLive = `INSERT OR IGNORE INTO acfunlive
(liveID, uid, name, streamName, startTime, title, duration, playbackURL, backupURL, liveCutNum)
VALUES
(?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`

// 更新直播时长
const updateDuration = `UPDATE acfunlive SET
duration = ?
WHERE liveID = ?;
`

// 根据uid查询
const selectUID = `SELECT * FROM acfunlive
WHERE uid = ?
ORDER BY startTime DESC
LIMIT ?;
`

const (
	selectLiveID      = `SELECT uid FROM acfunlive WHERE liveID = ?;`                   // 根据liveID查询
	createLiveIDIndex = `CREATE INDEX IF NOT EXISTS liveIDIndex ON acfunlive (liveID);` // 生成liveID的index
	createUIDIndex    = `CREATE INDEX IF NOT EXISTS uidIndex ON acfunlive (uid);`       // 生成uid的index
)

var (
	db            *sql.DB
	insertStmt    *sql.Stmt
	updateStmt    *sql.Stmt
	selectUIDStmt *sql.Stmt
)

// 插入live
func insert(ctx context.Context, l *live) {
	_, err := insertStmt.ExecContext(ctx,
		l.liveID, l.uid, l.name, l.streamName, l.startTime, l.title, l.duration, l.playbackURL, l.backupURL, l.liveCutNum,
	)
	checkErr(err)
}

// 更新直播时长
func update(ctx context.Context, liveID string, duration int64) {
	_, err := updateStmt.ExecContext(ctx, duration, liveID)
	checkErr(err)
}
