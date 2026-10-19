package main

import (
	"database/sql"
	"strconv"
	"time"
)

func (s *Store) expireOrders() error {
	var config string
	if e := s.db.QueryRow("SELECT value FROM settings WHERE key='orderTimeoutMinutes'").Scan(&config); e != nil {
		return e
	}
	minutes, e := strconv.Atoi(config)
	if e != nil {
		return e
	}
	cutoff := time.Now().Add(-time.Duration(minutes) * time.Minute).Format("2006-01-02 15:04:05")
	return s.transaction(func(tx *sql.Tx) error {
		rows, e := tx.Query("SELECT o.id FROM orders o JOIN channels c ON c.id=o.channel_id WHERE c.plugin='demo' AND o.status IN ('未支付','处理中') AND o.created_at<?", cutoff)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		for _, oid := range ids {
			if _, e = tx.Exec("UPDATE orders SET status='已关闭' WHERE id=? AND status IN ('未支付','处理中')", oid); e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT INTO audit(actor,action,target,detail,ip,created_at) VALUES(?,?,?,?,?,?)", "系统", "订单超时关闭", oid, "未支付超时", "local", timestamp()); e != nil {
				return e
			}
		}
		return nil
	})
}
