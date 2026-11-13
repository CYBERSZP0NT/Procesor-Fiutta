package router_test

import (
	"context"
	"testing"

	"github.com/CYBERSZP0NT/szmelc-router/pkg/router"
)

func TestModelLiveIsAgentLive(t *testing.T) {
	srv, _ := fakeUpstream(t)
	r := taskRouter(t, srv.URL, nil) // default mode Jako Tako
	if !r.IsTaskAlias("live") {
		t.Fatal(`model "live" must go through the task router`)
	}
	msgs := []router.Message{{Role: "user", Content: "hi"}}
	d, err := r.PlanTask(context.Background(), "live", msgs, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Mode != "live" || d.Winner.ID != "big" {
		t.Fatalf(`"live" must pick by richness: mode %s winner %+v`, d.Mode, d.Winner)
	}
	// A suffix still wins over the alias' own mode.
	if d, _ = r.PlanTask(context.Background(), "live:jako-tako", msgs, false, nil); d.Mode != "jako-tako" {
		t.Fatalf("suffix mode ignored: %s", d.Mode)
	}
	if _, err := r.SetTaskMode("po bogatości"); err != nil || r.TaskMode() != "live" {
		t.Fatalf("SetTaskMode(po bogatości): %v, mode %s", err, r.TaskMode())
	}
}
