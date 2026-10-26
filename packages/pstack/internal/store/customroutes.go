package store

import (
	"database/sql"
	"errors"
	"sort"
	"time"
)

// CustomRoute is one row of custom_routes: a stable alias name and the deployment it points at.
type CustomRoute struct {
	Name string
	// DeploymentID is "" when the route is unassigned (NULL in the table).
	DeploymentID         string
	CreatedAt, UpdatedAt int64
}

var (
	ErrCustomRouteExists   = errors.New("custom route exists")
	ErrCustomRouteNotFound = errors.New("custom route not found")
)

// ErrCustomRouteHeld refuses an unforced assign of a route another deployment holds.
type ErrCustomRouteHeld struct{ DeploymentID string }

func (e ErrCustomRouteHeld) Error() string { return "custom route held by " + e.DeploymentID }

const customRouteCols = "SELECT name, COALESCE(deployment_id, ''), created_at, updated_at FROM custom_routes"

// ListCustomRoutes returns every route, ordered by name.
func ListCustomRoutes(q Querier) ([]CustomRoute, error) {
	rows, err := q.Query(customRouteCols + " ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CustomRoute{}
	for rows.Next() {
		var r CustomRoute
		if err := rows.Scan(&r.Name, &r.DeploymentID, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCustomRoute reads one route; false when no route has that name.
func GetCustomRoute(q Querier, name string) (CustomRoute, bool, error) {
	var r CustomRoute
	err := q.QueryRow(customRouteCols+" WHERE name = ?", name).Scan(&r.Name, &r.DeploymentID, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CustomRoute{}, false, nil
	}
	return r, err == nil, err
}

// CreateCustomRoute adds a route, unassigned when deploymentID is "". ErrCustomRouteExists on a
// duplicate name.
func CreateCustomRoute(q Querier, name, deploymentID string) error {
	now := time.Now().UnixMilli()
	res, err := q.Exec(`INSERT INTO custom_routes (name, deployment_id, created_at, updated_at)
       VALUES (?, NULLIF(?, ''), ?, ?) ON CONFLICT(name) DO NOTHING`, name, deploymentID, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCustomRouteExists
	}
	return nil
}

// AssignCustomRoute points name at deploymentID and returns the previous holder ("" if none).
// Already held by deploymentID is a no-op. Held by another deployment is ErrCustomRouteHeld unless
// force. It reads then writes, so a forced move must run inside Store.Tx to be one transaction.
func AssignCustomRoute(q Querier, name, deploymentID string, force bool) (previous string, err error) {
	cur, ok, err := GetCustomRoute(q, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCustomRouteNotFound
	}
	if cur.DeploymentID == deploymentID {
		return cur.DeploymentID, nil
	}
	if cur.DeploymentID != "" && !force {
		return "", ErrCustomRouteHeld{cur.DeploymentID}
	}
	if err := setCustomRoute(q, name, deploymentID); err != nil {
		return "", err
	}
	return cur.DeploymentID, nil
}

// UnassignCustomRoute clears name's deployment and returns the previous holder ("" if none).
func UnassignCustomRoute(q Querier, name string) (previous string, err error) {
	cur, ok, err := GetCustomRoute(q, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCustomRouteNotFound
	}
	if cur.DeploymentID == "" {
		return "", nil
	}
	return cur.DeploymentID, setCustomRoute(q, name, "")
}

// DeleteCustomRoute removes name and returns the deployment that held it ("" if none).
func DeleteCustomRoute(q Querier, name string) (previous string, err error) {
	err = q.QueryRow("DELETE FROM custom_routes WHERE name = ? RETURNING COALESCE(deployment_id, '')", name).Scan(&previous)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCustomRouteNotFound
	}
	return previous, err
}

// UnassignDeploymentRoutes unassigns every route deploymentID holds and returns their names, sorted.
func UnassignDeploymentRoutes(q Querier, deploymentID string) ([]string, error) {
	rows, err := q.Query("UPDATE custom_routes SET deployment_id = NULL, updated_at = ? WHERE deployment_id = ? RETURNING name",
		time.Now().UnixMilli(), deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	sort.Strings(names) // RETURNING order is unspecified
	return names, rows.Err()
}

func setCustomRoute(q Querier, name, deploymentID string) error {
	_, err := q.Exec("UPDATE custom_routes SET deployment_id = NULLIF(?, ''), updated_at = ? WHERE name = ?",
		deploymentID, time.Now().UnixMilli(), name)
	return err
}
