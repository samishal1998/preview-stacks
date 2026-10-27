package cli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// `pstack route` — a thin client over /api/custom-routes, addressed like `pstack api`.

const routeUsage = "usage: pstack route ls | create <name> [<deployment>] | assign <name> <deployment> | unassign <name> | rm <name>   [--force] [--json]"

// routeBody is what create and assign send; an absent field is an unset one.
type routeBody struct {
	Name       string `json:"name,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Force      bool   `json:"force,omitempty"`
}

// routeRow is the API's route, as much of it as a line prints.
type routeRow struct {
	Name       string   `json:"name"`
	Deployment *string  `json:"deployment"`
	Stack      *string  `json:"stack"`
	Hosts      []string `json:"hosts"`
	// Blocked is each host something else serves; nil from a server older than it.
	Blocked []struct {
		Host string `json:"host"`
		By   struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"by"`
	} `json:"blocked"`
}

// line is `checkout  myapp-pr-123  web-checkout.<domain>, …` or `checkout  (unassigned)`, then one
// `  blocked: <host> (<kind> <name>)` line per host it does not serve.
func (r routeRow) line() string {
	if r.Deployment == nil {
		return r.Name + "  (unassigned)"
	}
	who := *r.Deployment
	if r.Stack != nil {
		who = *r.Stack
	}
	out := r.Name + "  " + who
	if len(r.Hosts) > 0 {
		out += "  " + strings.Join(r.Hosts, ", ")
	}
	for _, b := range r.Blocked {
		out += "\n  blocked: " + b.Host + " (" + b.By.Kind + " " + b.By.Name + ")"
	}
	return out
}

func routeCmd(args *Parsed, ios IO) *Exit {
	name, more := args.Arg, args.Extra
	one := name != "" && len(more) == 0
	path := "/api/custom-routes"
	var method string
	var body *routeBody
	switch {
	case args.Sub == "ls" && name == "":
		method = "GET"
	case args.Sub == "create" && name != "" && len(more) <= 1:
		method, body = "POST", &routeBody{Name: name, Force: args.Force}
		if len(more) == 1 {
			body.Deployment = more[0]
		}
	case args.Sub == "assign" && name != "" && len(more) == 1:
		method, path, body = "PUT", path+"/"+url.PathEscape(name)+"/assignment", &routeBody{Deployment: more[0], Force: args.Force}
	case args.Sub == "unassign" && one:
		method, path = "DELETE", path+"/"+url.PathEscape(name)+"/assignment"
	case args.Sub == "rm" && one:
		method, path = "DELETE", path+"/"+url.PathEscape(name)
	default:
		return fail(routeUsage)
	}
	base, token, ex := apiBaseAnyRole(ios.Env)
	if ex != nil {
		return ex
	}
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	status, got, err := apiCall(method, base+path, token, payload)
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if status < 200 || status >= 300 {
		var held struct {
			Error      string           `json:"error"`
			AssignedTo *json.RawMessage `json:"assignedTo"`
		}
		if status == 409 && json.Unmarshal(got, &held) == nil && held.AssignedTo != nil && held.Error != "" {
			return &Exit{Code: ExitFailed, Msg: held.Error + " (--force moves it)"}
		}
		return &Exit{Code: ExitFailed, Msg: apiError(status, got)}
	}
	out := ios.Stdout
	if args.JSON {
		fmt.Fprintln(out, strings.TrimSpace(string(got)))
		return nil
	}
	if args.Sub == "rm" {
		fmt.Fprintln(out, name+"  deleted")
		return nil
	}
	var rows []routeRow
	if args.Sub == "ls" {
		err = json.Unmarshal(got, &rows)
	} else {
		var r routeRow
		err = json.Unmarshal(got, &r)
		rows = append(rows, r)
	}
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: "the API answered with a body this pstack does not understand (" + err.Error() + ")"}
	}
	for _, r := range rows {
		fmt.Fprintln(out, r.line())
	}
	return nil
}
