package store

// SetCustomRouteSubdomains sets route name's wildcard depth ("" off). updated_at stays: it orders
// claims between routes, and a depth change is not a new claim. ErrCustomRouteNotFound when absent.
func SetCustomRouteSubdomains(q Querier, name, depth string) error {
	res, err := q.Exec("UPDATE custom_routes SET subdomains = ? WHERE name = ?", depth, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCustomRouteNotFound
	}
	return nil
}

// DeploymentSubdomains is deployment id's per-service wildcard depths, service → depth.
func DeploymentSubdomains(q Querier, id string) (map[string]string, error) {
	rows, err := q.Query("SELECT service, depth FROM deployment_subdomains WHERE deployment_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var svc, depth string
		if err := rows.Scan(&svc, &depth); err != nil {
			return nil, err
		}
		out[svc] = depth
	}
	return out, rows.Err()
}

// SetDeploymentSubdomain sets one service's depth; "" removes it.
func SetDeploymentSubdomain(q Querier, id, service, depth string) error {
	if depth == "" {
		_, err := q.Exec("DELETE FROM deployment_subdomains WHERE deployment_id = ? AND service = ?", id, service)
		return err
	}
	_, err := q.Exec(`INSERT INTO deployment_subdomains (deployment_id, service, depth) VALUES (?, ?, ?)
		ON CONFLICT (deployment_id, service) DO UPDATE SET depth = excluded.depth`, id, service, depth)
	return err
}

// ForgetDeploymentSubdomains drops every row of deployment id, for its removal.
func ForgetDeploymentSubdomains(q Querier, id string) error {
	_, err := q.Exec("DELETE FROM deployment_subdomains WHERE deployment_id = ?", id)
	return err
}
