package cli

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// `pstack forward` — a thin client over /api/forward, addressed like `pstack route`. The token for
// the target comes from PSTACK_FORWARD_TOKEN only: argv is readable through `ps`.

const forwardUsage = "usage: pstack forward | forward set <url> [--via <ip[:port]>] | forward off"

// forwardBody is what set sends. Field order is the wire order.
type forwardBody struct {
	To    string `json:"to"`
	Via   string `json:"via,omitempty"`
	Token string `json:"token"`
}

func forwardCmd(args *Parsed, ios IO) *Exit {
	if len(args.Extra) > 0 || (args.Sub != "set" && (args.Arg != "" || args.Via != "")) {
		return fail(forwardUsage)
	}
	var method string
	var payload []byte
	switch args.Sub {
	case "":
		method = "GET"
	case "set":
		if args.Arg == "" {
			return fail(forwardUsage)
		}
		tok, _ := ios.Env("PSTACK_FORWARD_TOKEN")
		if tok == "" {
			return fail("set PSTACK_FORWARD_TOKEN — a user token on " + args.Arg + ", developer or higher")
		}
		method = "PUT"
		payload, _ = json.Marshal(forwardBody{To: args.Arg, Via: args.Via, Token: tok})
	case "off":
		method = "DELETE"
	default:
		return fail(forwardUsage)
	}
	base, token, ex := apiBaseAnyRole(ios.Env)
	if ex != nil {
		return ex
	}
	status, got, err := apiCall(method, base+"/api/forward", token, payload, nil)
	if err != nil {
		return &Exit{Code: ExitFailed, Msg: err.Error()}
	}
	if status < 200 || status >= 300 {
		return &Exit{Code: ExitFailed, Msg: apiError(status, got)}
	}
	if method == "DELETE" {
		fmt.Fprintln(ios.Stdout, "Not forwarding.")
		return nil
	}
	var row *struct {
		To       string `json:"to"`
		LastOkAt int64  `json:"lastOkAt"`
	}
	if err := json.Unmarshal(got, &row); err != nil {
		return &Exit{Code: ExitFailed, Msg: "the API answered with a body this pstack does not understand (" + err.Error() + ")"}
	}
	if row == nil {
		fmt.Fprintln(ios.Stdout, "Not forwarding.")
		return nil
	}
	fmt.Fprintln(ios.Stdout, "Forwarding to "+row.To+" · last ok "+ago(row.LastOkAt, time.Now().UnixMilli()))
	return nil
}

// ago is the Hearth UI's floor format (apps/ui-hearth/src/format.ts), so the CLI and the Control
// page say the same thing.
func ago(at, now int64) string {
	s := (now - at + 500) / 1000 // rounded to the second, as format.ts does
	switch {
	case s < 60:
		return "just now"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m ago"
	case s < 86400:
		return strconv.FormatInt(s/3600, 10) + "h ago"
	}
	return strconv.FormatInt(s/86400, 10) + "d ago"
}
