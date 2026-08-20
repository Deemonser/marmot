package snapshot

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"example.com/marmot/internal/domain/scan"
	_ "github.com/mattn/go-sqlite3"
)

type Node = scan.Node

type Snapshot = scan.Snapshot

type DirectorySize = scan.DirectorySize

const schemaVersion = 3

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("snapshot schema version %d is newer than supported version %d", version, schemaVersion)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if version == 0 {
		for _, statement := range []string{
			`CREATE TABLE IF NOT EXISTS snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL,
			state TEXT NOT NULL,
			phase TEXT NOT NULL DEFAULT 'catalog',
			root TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			finished_at INTEGER,
			node_count INTEGER NOT NULL DEFAULT 0,
			file_count INTEGER NOT NULL DEFAULT 0,
			dir_count INTEGER NOT NULL DEFAULT 0,
			bytes INTEGER NOT NULL DEFAULT 0,
			issue_count INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT ''
		)`,
			`CREATE TABLE IF NOT EXISTS scan_nodes (
			snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
			node_id INTEGER NOT NULL,
			parent_id INTEGER NOT NULL,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			logical_size INTEGER NOT NULL,
			allocated_size INTEGER NOT NULL,
			owned_allocated INTEGER NOT NULL,
			confidence TEXT NOT NULL,
			size_basis TEXT NOT NULL,
			device INTEGER NOT NULL,
			inode INTEGER NOT NULL,
			modified_at INTEGER NOT NULL,
			has_children INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (snapshot_id, node_id)
		)`,
			"CREATE INDEX IF NOT EXISTS idx_scan_nodes_parent ON scan_nodes(snapshot_id, parent_id, owned_allocated DESC, node_id)",
		} {
			if _, err := tx.Exec(statement); err != nil {
				return rollback(err)
			}
		}
	} else if version == 1 {
		if _, err := tx.Exec(`ALTER TABLE snapshots ADD COLUMN task_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return rollback(err)
		}
		if _, err := tx.Exec(`ALTER TABLE snapshots ADD COLUMN phase TEXT NOT NULL DEFAULT 'catalog'`); err != nil {
			return rollback(err)
		}
	} else if version == 2 {
		if _, err := tx.Exec(`ALTER TABLE snapshots ADD COLUMN phase TEXT NOT NULL DEFAULT 'catalog'`); err != nil {
			return rollback(err)
		}
	}
	if version < schemaVersion {
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
			return rollback(err)
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateSnapshot(taskID, root string) (int64, error) {
	result, err := s.db.Exec("INSERT INTO snapshots(task_id, state, phase, root, created_at) VALUES (?, 'running', 'catalog', ?, ?)", taskID, root, time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateSnapshotPhase(snapshotID int64, phase string) error {
	_, err := s.db.Exec("UPDATE snapshots SET phase = ? WHERE id = ? AND state = 'running'", phase, snapshotID)
	return err
}

func (s *Store) InsertNodes(snapshotID int64, nodes []Node) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO scan_nodes
		(snapshot_id, node_id, parent_id, path, name, kind, logical_size, allocated_size,
		 owned_allocated, confidence, size_basis, device, inode, modified_at, has_children)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for _, node := range nodes {
		if _, err := stmt.Exec(snapshotID, node.ID, node.ParentID, node.Path, node.Name, node.Kind,
			node.LogicalSize, node.AllocatedSize, node.OwnedAllocated, node.Confidence, node.SizeBasis,
			node.Device, node.Inode, node.ModifiedAt.UnixNano(), node.HasChildren); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateDirectorySizes(snapshotID int64, sizes map[int64]DirectorySize) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE scan_nodes SET logical_size = ?, allocated_size = ?, owned_allocated = ?, confidence = ?, size_basis = ? WHERE snapshot_id = ? AND node_id = ? AND kind = 'directory'`)
	if err != nil {
		tx.Rollback()
		return err
	}
	for nodeID, size := range sizes {
		basis := size.SizeBasis
		if basis == "" {
			basis = "descendant_sum_v1"
		}
		if _, err := stmt.Exec(size.LogicalSize, size.AllocatedSize, size.OwnedAllocated, size.Confidence, basis, snapshotID, nodeID); err != nil {
			stmt.Close()
			tx.Rollback()
			return err
		}
	}
	if err := stmt.Close(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) NodeByPath(snapshotID int64, path string) (Node, error) {
	row := s.db.QueryRow(`SELECT node_id, parent_id, path, name, kind, logical_size, allocated_size, owned_allocated, confidence, size_basis, device, inode, modified_at, has_children FROM scan_nodes WHERE snapshot_id = ? AND path = ?`, snapshotID, path)
	var node Node
	var modified int64
	var hasChildren int
	if err := row.Scan(&node.ID, &node.ParentID, &node.Path, &node.Name, &node.Kind, &node.LogicalSize, &node.AllocatedSize, &node.OwnedAllocated, &node.Confidence, &node.SizeBasis, &node.Device, &node.Inode, &modified, &hasChildren); err != nil {
		return Node{}, err
	}
	node.ModifiedAt = time.Unix(0, modified)
	node.HasChildren = hasChildren != 0
	return node, nil
}

func (s *Store) FinishSnapshot(snapshotID int64, state, failure string, nodeCount, fileCount, dirCount, bytes, issues int64) error {
	_, err := s.db.Exec(`UPDATE snapshots SET state = ?, finished_at = ?, error = ?, node_count = ?, file_count = ?, dir_count = ?, bytes = ?, issue_count = ? WHERE id = ?`, state, time.Now().UnixNano(), failure, nodeCount, fileCount, dirCount, bytes, issues, snapshotID)
	return err
}

func (s *Store) MarkRunningInterrupted() error {
	_, err := s.db.Exec(`UPDATE snapshots SET state = 'interrupted', finished_at = ?, error = ? WHERE state = 'running'`, time.Now().UnixNano(), "scan interrupted when application exited")
	return err
}

func (s *Store) SnapshotByTaskID(taskID string) (scan.Snapshot, error) {
	row := s.db.QueryRow(`SELECT task_id, id, state, phase, root, node_count, file_count, dir_count, bytes, issue_count, error FROM snapshots WHERE task_id = ? ORDER BY id DESC LIMIT 1`, taskID)
	var snapshot scan.Snapshot
	if err := row.Scan(&snapshot.TaskID, &snapshot.ID, &snapshot.State, &snapshot.Phase, &snapshot.Root, &snapshot.NodeCount, &snapshot.FileCount, &snapshot.DirCount, &snapshot.Bytes, &snapshot.Issues, &snapshot.Error); err != nil {
		return scan.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) Children(snapshotID, parentID int64, limit, offset int) ([]Node, error) {
	rows, err := s.db.Query(`SELECT node_id, parent_id, path, name, kind, logical_size, allocated_size, owned_allocated, confidence, size_basis, device, inode, modified_at, has_children FROM scan_nodes WHERE snapshot_id = ? AND parent_id = ? ORDER BY owned_allocated DESC, node_id LIMIT ? OFFSET ?`, snapshotID, parentID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nodes []Node
	for rows.Next() {
		var node Node
		var modified int64
		var hasChildren int
		if err := rows.Scan(&node.ID, &node.ParentID, &node.Path, &node.Name, &node.Kind, &node.LogicalSize, &node.AllocatedSize, &node.OwnedAllocated, &node.Confidence, &node.SizeBasis, &node.Device, &node.Inode, &modified, &hasChildren); err != nil {
			return nil, err
		}
		node.ModifiedAt = time.Unix(0, modified)
		node.HasChildren = hasChildren != 0
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}
