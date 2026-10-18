package api

import (
	"testing"

	"github.com/samishal1998/preview-stacks/packages/pstack/internal/routing"
)

func TestConsoleName(t *testing.T) {
	// negative control: ignore ui — the hearth row reports "advanced".
	for _, c := range []struct{ service, ui, want string }{
		{"", "", "basic"},
		{routing.AdvancedUIService, "", "advanced"},
		{routing.AdvancedUIService, "hearth", "hearth"},
	} {
		if got := consoleName(c.service, c.ui); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}
