package orgserver

import (
	"encoding/json"
	"time"

	"contextdrop.dev/context-drop/internal/db"
	"contextdrop.dev/context-drop/internal/pool"
)

// Presence is shared through the database, so any server can list an org's agents and find the server
// holding a machine's connection. Each server marks itself alive every heartbeat; a machine whose
// server stopped beating is offline.

// heartbeat is how often a server marks itself alive, and serverTTL how long until a silent server's
// machines count as offline.
const (
	heartbeat = 10 * time.Second
	serverTTL = 35 * time.Second
)

// Beat marks a server alive.
func (s *Store) Beat(server string) error {
	_, err := s.db.Exec(`INSERT INTO servers VALUES (?, ?) ON CONFLICT (id) DO UPDATE SET seen = excluded.seen`, server, time.Now().UnixMilli())
	return err
}

// Stopped forgets a server and marks its machines offline, for a clean shutdown.
func (s *Store) Stopped(server string) error {
	if _, err := s.db.Exec(`UPDATE presence SET server = '', conn = '' WHERE server = ?`, server); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM servers WHERE id = ?`, server)
	return err
}

// Connected records that a machine's connection conn is held by server. Its last agents are kept until
// it reports again.
func (s *Store) Connected(org, machine, server, conn string) error {
	_, err := s.db.Exec(`INSERT INTO presence VALUES (?, ?, ?, ?, '[]', ?)
		ON CONFLICT (org, machine) DO UPDATE SET server = excluded.server, conn = excluded.conn, seen = excluded.seen`,
		org, machine, server, conn, time.Now().UnixMilli())
	return err
}

// Disconnected marks a machine offline, unless a newer connection has replaced conn.
func (s *Store) Disconnected(org, machine, conn string) error {
	_, err := s.db.Exec(`UPDATE presence SET server = '', conn = '' WHERE org = ? AND machine = ? AND conn = ?`, org, machine, conn)
	return err
}

// Report replaces a machine's agents, for connection conn.
func (s *Store) Report(org, machine, conn string, agents []pool.Agent) error {
	raw, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE presence SET agents = ?, seen = ? WHERE org = ? AND machine = ? AND conn = ?`,
		string(raw), time.Now().UnixMilli(), org, machine, conn)
	return err
}

// Forget drops a machine's presence, when it leaves the org.
func (s *Store) Forget(org, machine string) error {
	_, err := s.db.Exec(`DELETE FROM presence WHERE org = ? AND machine = ?`, org, machine)
	return err
}

// Where returns the live server holding a machine's connection; found is false when none does.
func (s *Store) Where(org, machine string) (server string, found bool, err error) {
	err = s.db.QueryRow(`SELECT p.server FROM presence p JOIN servers v ON v.id = p.server
		WHERE p.org = ? AND p.machine = ? AND v.seen > ?`, org, machine, time.Now().Add(-serverTTL).UnixMilli()).Scan(&server)
	if db.NoRows(err) {
		return "", false, nil
	}
	return server, err == nil && server != "", err
}

// Holder returns the connection presence records for a machine, live or not.
func (s *Store) Holder(org, machine string) (conn string, found bool, err error) {
	err = s.db.QueryRow(`SELECT conn FROM presence WHERE org = ? AND machine = ?`, org, machine).Scan(&conn)
	if db.NoRows(err) {
		return "", false, nil
	}
	return conn, err == nil && conn != "", err
}

// Member reports whether a machine still belongs to the org.
func (s *Store) Member(org, machine string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM machines WHERE org = ? AND name = ?`, org, machine).Scan(&n)
	return n > 0, err
}

// Holding returns the IDs of the connections presence says server holds.
func (s *Store) Holding(server string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT conn FROM presence WHERE server = ?`, server)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	held := map[string]bool{}
	for rows.Next() {
		var conn string
		if err := rows.Scan(&conn); err != nil {
			return nil, err
		}
		held[conn] = true
	}
	return held, rows.Err()
}

// Agents returns every agent the org's machines last reported; agents of a machine with no live
// connection are offline.
func (s *Store) Agents(org string) ([]pool.Agent, error) {
	rows, err := s.db.Query(`SELECT p.agents, CASE WHEN v.seen > ? THEN 1 ELSE 0 END FROM presence p
		LEFT JOIN servers v ON v.id = p.server WHERE p.org = ?`, time.Now().Add(-serverTTL).UnixMilli(), org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pool.Agent
	for rows.Next() {
		var raw string
		var live int
		if err := rows.Scan(&raw, &live); err != nil {
			return nil, err
		}
		var list []pool.Agent
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			continue
		}
		for _, a := range list {
			if live == 0 {
				a.Status = pool.StatusOffline
			}
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

// Sweep forgets servers silent for a day and old relay rows and webhook IDs.
func (s *Store) Sweep() error {
	now := time.Now()
	if _, err := s.db.Exec(`DELETE FROM servers WHERE seen < ?`, now.Add(-24*time.Hour).UnixMilli()); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM relay WHERE created < ?`, now.Add(-time.Hour).UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM extension_once WHERE at < ?`, now.Add(-onceTTL).UnixMilli())
	return err
}
