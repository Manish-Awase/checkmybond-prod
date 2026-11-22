package alerting

import (
	"strings"
	"testing"
	"time"
)

func testRule() Rule {
	return Rule{
		ID: "r1", Name: "API error rate", Metric: "http_error_rate_pct",
		Comparator: "gt", Threshold: 5, Severity: SeverityCritical,
		WindowSeconds: 300, ForSeconds: 120, CooldownSeconds: 1800,
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		v    float64
		cmp  string
		th   float64
		want bool
	}{
		{10, "gt", 5, true},
		{5, "gt", 5, false},
		{5, "gte", 5, true},
		{0, "lt", 1, true},
		{1, "lt", 1, false},
		{1, "lte", 1, true},
		{3, "eq", 3, true},
		{3, "eq", 4, false},
		{3, "nonsense", 1, false},
	}
	for _, c := range cases {
		if got := Compare(c.v, c.cmp, c.th); got != c.want {
			t.Errorf("Compare(%v,%q,%v) = %v, want %v", c.v, c.cmp, c.th, got, c.want)
		}
	}
}

// The core behaviour: a breach shorter than ForSeconds must never email.
func TestBlipDoesNotFire(t *testing.T) {
	r := testRule()
	st := ruleState{}
	t0 := time.Now()

	act, st, _ := decideTransition(st, true, t0, r)
	if act != actionNone {
		t.Fatalf("first breaching tick = %v, want none", act)
	}
	// Still breaching, but only 60s in against a 120s ForSeconds.
	act, st, _ = decideTransition(st, true, t0.Add(60*time.Second), r)
	if act != actionNone {
		t.Fatalf("60s into a 120s hold = %v, want none", act)
	}
	// Recovered before the hold elapsed — nothing should ever be sent.
	act, st, _ = decideTransition(st, false, t0.Add(90*time.Second), r)
	if act != actionNone {
		t.Fatalf("recovery before firing = %v, want none", act)
	}
	if st.firing {
		t.Fatal("state says firing after a blip")
	}
	if !st.breachingSince.IsZero() {
		t.Fatal("breach timer not cleared on recovery")
	}
}

func TestFiresAfterSustainedBreach(t *testing.T) {
	r := testRule()
	st := ruleState{}
	t0 := time.Now()

	_, st, _ = decideTransition(st, true, t0, r)
	act, st, eventID := decideTransition(st, true, t0.Add(120*time.Second), r)
	if act != actionFire {
		t.Fatalf("after the hold elapsed = %v, want fire", act)
	}
	if eventID == "" {
		t.Fatal("fire produced no event id")
	}
	if !st.firing {
		t.Fatal("state not marked firing")
	}
}

func TestFiresImmediatelyWhenForSecondsIsZero(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	act, st, _ := decideTransition(ruleState{}, true, time.Now(), r)
	if act != actionFire {
		t.Fatalf("with ForSeconds=0 the first breach = %v, want fire", act)
	}
	if !st.firing {
		t.Fatal("not firing")
	}
}

func TestDoesNotRenotifyInsideCooldown(t *testing.T) {
	r := testRule()
	t0 := time.Now()
	st := ruleState{}
	_, st, _ = decideTransition(st, true, t0, r)
	act, st, _ := decideTransition(st, true, t0.Add(120*time.Second), r)
	if act != actionFire {
		t.Fatalf("setup failed: %v", act)
	}

	// 29 minutes later, still inside the 30-minute cooldown.
	act, st, _ = decideTransition(st, true, t0.Add(120*time.Second+29*time.Minute), r)
	if act != actionNone {
		t.Fatalf("inside cooldown = %v, want none", act)
	}
	// Past the cooldown.
	act, _, id := decideTransition(st, true, t0.Add(120*time.Second+31*time.Minute), r)
	if act != actionRenotify {
		t.Fatalf("past cooldown = %v, want renotify", act)
	}
	if id == "" {
		t.Fatal("renotify lost the event id")
	}
}

func TestRenotifyKeepsTheSameEventID(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	t0 := time.Now()
	act, st, firstID := decideTransition(ruleState{}, true, t0, r)
	if act != actionFire {
		t.Fatal("setup")
	}
	act, _, secondID := decideTransition(st, true, t0.Add(31*time.Minute), r)
	if act != actionRenotify {
		t.Fatalf("got %v", act)
	}
	if firstID != secondID {
		t.Fatalf("renotify changed event id: %q -> %q — that would open a second incident", firstID, secondID)
	}
}

func TestResolveEmitsOnceAndCarriesEventID(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	t0 := time.Now()
	_, st, firedID := decideTransition(ruleState{}, true, t0, r)

	act, st, resolvedID := decideTransition(st, false, t0.Add(time.Minute), r)
	if act != actionResolve {
		t.Fatalf("recovery = %v, want resolve", act)
	}
	if resolvedID != firedID {
		t.Fatalf("resolve closed the wrong event: %q, want %q", resolvedID, firedID)
	}
	if st.firing || st.eventID != "" {
		t.Fatal("state not cleared after resolve")
	}

	// A second healthy tick must stay silent.
	act, _, _ = decideTransition(st, false, t0.Add(2*time.Minute), r)
	if act != actionNone {
		t.Fatalf("second healthy tick = %v, want none", act)
	}
}

func TestResolveIgnoresCooldown(t *testing.T) {
	// A recovery must be announced immediately, even one second after
	// firing — an incident left looking open is expensive.
	r := testRule()
	r.ForSeconds = 0
	r.CooldownSeconds = 86400
	t0 := time.Now()
	_, st, _ := decideTransition(ruleState{}, true, t0, r)
	act, _, _ := decideTransition(st, false, t0.Add(time.Second), r)
	if act != actionResolve {
		t.Fatalf("resolve was suppressed by cooldown: %v", act)
	}
}

func TestFlappingRefiresAfterRecovery(t *testing.T) {
	r := testRule()
	r.ForSeconds = 0
	t0 := time.Now()

	_, st, id1 := decideTransition(ruleState{}, true, t0, r)
	_, st, _ = decideTransition(st, false, t0.Add(time.Minute), r)
	act, _, id2 := decideTransition(st, true, t0.Add(2*time.Minute), r)

	if act != actionFire {
		t.Fatalf("re-breach after recovery = %v, want fire", act)
	}
	if id1 == id2 {
		t.Fatal("second incident reused the first incident's id")
	}
}

func TestHoldTimerResetsOnRecovery(t *testing.T) {
	// Breach for 90s, recover, breach again — the second breach must
	// start its 120s hold over rather than inheriting the first 90s.
	r := testRule()
	t0 := time.Now()
	st := ruleState{}
	_, st, _ = decideTransition(st, true, t0, r)
	_, st, _ = decideTransition(st, false, t0.Add(90*time.Second), r)
	_, st, _ = decideTransition(st, true, t0.Add(91*time.Second), r)

	act, _, _ := decideTransition(st, true, t0.Add(150*time.Second), r)
	if act != actionNone {
		t.Fatalf("hold timer was not reset — fired after only 59s of the second breach (%v)", act)
	}
}

func TestSeverityRouting(t *testing.T) {
	// A warning-level recipient must not be woken by info alerts, but
	// must receive critical ones.
	cases := []struct {
		recipientMin string
		alert        string
		wantReceive  bool
	}{
		{SeverityWarning, SeverityInfo, false},
		{SeverityWarning, SeverityWarning, true},
		{SeverityWarning, SeverityCritical, true},
		{SeverityCritical, SeverityWarning, false},
		{SeverityCritical, SeverityCritical, true},
		{SeverityInfo, SeverityInfo, true},
	}
	for _, c := range cases {
		got := severityRank(c.recipientMin) <= severityRank(c.alert)
		if got != c.wantReceive {
			t.Errorf("recipient min=%s alert=%s: receive=%v, want %v",
				c.recipientMin, c.alert, got, c.wantReceive)
		}
	}
}

func TestRuleValidateRejectsUnknownMetric(t *testing.T) {
	r := testRule()
	r.Metric = "definitely_not_a_metric"
	if err := r.Validate(); err == nil {
		t.Fatal("unknown metric accepted — the engine would silently never evaluate this rule")
	}
}

func TestRuleValidateAcceptsEveryCatalogMetric(t *testing.T) {
	for key := range Catalog {
		r := testRule()
		r.Metric = key
		if err := r.Validate(); err != nil {
			t.Errorf("catalog metric %q rejected by Validate: %v", key, err)
		}
	}
}

func TestRuleValidateDefaults(t *testing.T) {
	r := Rule{Name: " My rule ", Metric: "queue_depth", Threshold: 10}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Name != "My rule" {
		t.Errorf("name not trimmed: %q", r.Name)
	}
	if r.Comparator != "gt" {
		t.Errorf("comparator default = %q", r.Comparator)
	}
	if r.Severity != SeverityWarning {
		t.Errorf("severity default = %q", r.Severity)
	}
	if r.CooldownSeconds != 1800 {
		t.Errorf("cooldown default = %d — without one, a firing rule emails every tick", r.CooldownSeconds)
	}
	if r.WindowSeconds != 300 {
		t.Errorf("window default = %d", r.WindowSeconds)
	}
}

func TestRuleValidateRejectsBadNameAndSeverity(t *testing.T) {
	if err := (&Rule{Metric: "queue_depth"}).Validate(); err == nil {
		t.Error("empty name accepted")
	}
	r := testRule()
	r.Severity = "apocalyptic"
	if err := r.Validate(); err == nil {
		t.Error("bad severity accepted")
	}
	r = testRule()
	r.Comparator = "~="
	if err := r.Validate(); err == nil {
		t.Error("bad comparator accepted")
	}
}

func TestEveryCatalogEntryIsWellFormed(t *testing.T) {
	for key, r := range Catalog {
		if r.Key != key {
			t.Errorf("catalog key %q has Key field %q", key, r.Key)
		}
		if r.Fn == nil {
			t.Errorf("catalog entry %q has no resolver function", key)
		}
		if r.Label == "" || r.Description == "" {
			t.Errorf("catalog entry %q is missing a label or description — the UI would show a blank option", key)
		}
		if !ValidComparator(r.Comparator) {
			t.Errorf("catalog entry %q suggests invalid comparator %q", key, r.Comparator)
		}
		switch r.Unit {
		case "percent", "ms", "seconds", "minutes", "count", "per_min", "bool", "ratio":
		default:
			t.Errorf("catalog entry %q has unknown unit %q — formatValue would fall through", key, r.Unit)
		}
		if r.Source != "sql" && r.Source != "process" {
			t.Errorf("catalog entry %q has unknown source %q", key, r.Source)
		}
	}
}

func TestCatalogListIsSorted(t *testing.T) {
	list := CatalogList()
	if len(list) != len(Catalog) {
		t.Fatalf("CatalogList returned %d of %d entries", len(list), len(Catalog))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Key > list[i].Key {
			t.Fatalf("not sorted at %d: %q then %q", i, list[i-1].Key, list[i].Key)
		}
	}
}

func TestPctHandlesZeroTraffic(t *testing.T) {
	if got := pct(0, 0); got != 0 {
		t.Fatalf("pct(0,0) = %v, want 0 — NaN would silently disable the rule", got)
	}
	if got := pct(5, 10); got != 50 {
		t.Fatalf("pct(5,10) = %v, want 50", got)
	}
}

func TestFormatValueUsesUnits(t *testing.T) {
	cases := []struct {
		metric string
		v      float64
		want   string
	}{
		{"http_error_rate_pct", 12.34, "12.3%"},
		{"http_latency_p95_ms", 2431, "2431 ms"},
		{"queue_depth", 140, "140"},
		{"db_up", 0, "unreachable"},
		{"db_up", 1, "reachable"},
		{"coupon_redemptions_per_min", 22.5, "22.5/min"},
		{"db_pool_utilization", 0.87, "0.87"},
		{"extraction_p95_seconds", 310.4, "310.4 s"},
	}
	for _, c := range cases {
		r := Rule{Metric: c.metric}
		if got := formatValue(r, c.v); got != c.want {
			t.Errorf("formatValue(%s, %v) = %q, want %q", c.metric, c.v, got, c.want)
		}
	}
}

func TestEmailSubjectIsScannable(t *testing.T) {
	n := &EmailNotifier{AppName: "CheckMyBond"}
	r := testRule()

	firing := n.subject(Notification{Rule: r, Observed: 12.4})
	if !strings.Contains(firing, "CRITICAL") || !strings.Contains(firing, "12.4%") {
		t.Fatalf("subject missing severity or value: %q", firing)
	}
	if !strings.Contains(firing, "API error rate") {
		t.Fatalf("subject missing rule name: %q", firing)
	}

	resolved := n.subject(Notification{Rule: r, Observed: 0.2, Resolved: true})
	if !strings.Contains(resolved, "RESOLVED") {
		t.Fatalf("resolved subject not marked: %q", resolved)
	}
}

func TestEmailBodyContainsActionableDetail(t *testing.T) {
	n := &EmailNotifier{AppName: "CheckMyBond", DashboardURL: "https://grafana.example.com/d/bondqi"}
	r := testRule()
	desc := "Share of requests returning 5xx."
	r.Description = &desc

	body := n.body(Notification{
		Rule: r, Observed: 12.4, TraceID: "abc123",
		Event: Event{StartedAt: time.Now()},
	})

	for _, want := range []string{"12.4%", "5.0%", "critical", "http_error_rate_pct", "abc123", "grafana.example.com", "Where to look first"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestResolvedEmailStatesNoActionNeeded(t *testing.T) {
	n := &EmailNotifier{}
	body := n.body(Notification{
		Rule: testRule(), Observed: 0.4, Resolved: true,
		Event: Event{StartedAt: time.Now().Add(-45 * time.Minute)},
	})
	if !strings.Contains(body, "recovered") {
		t.Errorf("resolved body should say it recovered:\n%s", body)
	}
	if !strings.Contains(body, "45 minutes") {
		t.Errorf("resolved body should report the duration:\n%s", body)
	}
}

func TestEveryCatalogMetricHasATriageHint(t *testing.T) {
	// Not strictly required, but a missing hint is a worse email, so this
	// flags new metrics added without one.
	missing := []string{}
	for key := range Catalog {
		if triageHint(key) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Logf("metrics without a triage hint (consider adding one): %s", strings.Join(missing, ", "))
	}
}

func TestHumanizeDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second: "30 seconds",
		5 * time.Minute:  "5 minutes",
		2 * time.Hour:    "2 hours",
		90 * time.Minute: "1h 30m",
		50 * time.Hour:   "2.1 days",
	}
	for d, want := range cases {
		if got := humanizeDuration(d); got != want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestLooksLikeEmail(t *testing.T) {
	good := []string{"a@b.co", "ops.team@example.com", "first+tag@sub.domain.org"}
	bad := []string{"", "notanemail", "@example.com", "a@b", "two @spaces.com", "a@b.com,c@d.com"}
	for _, s := range good {
		if !looksLikeEmail(s) {
			t.Errorf("rejected valid address %q", s)
		}
	}
	for _, s := range bad {
		if looksLikeEmail(s) {
			t.Errorf("accepted invalid address %q", s)
		}
	}
}

func TestParseLabelsToleratesGarbage(t *testing.T) {
	bad := "{not json"
	if got := parseLabels(&bad); got != nil {
		t.Fatalf("malformed labels should parse to nil, got %v", got)
	}
	good := `{"route":"/api/x"}`
	m := parseLabels(&good)
	if m["route"] != "/api/x" {
		t.Fatalf("labels not parsed: %v", m)
	}
	if parseLabels(nil) != nil {
		t.Fatal("nil labels should stay nil")
	}
}

func TestEncodeLabelsRoundTrip(t *testing.T) {
	in := map[string]string{"route": "/api/documents/upload", "service": "hitl"}
	enc := encodeLabels(in)
	if enc == nil {
		t.Fatal("encode returned nil")
	}
	out := parseLabels(enc)
	if out["route"] != in["route"] || out["service"] != in["service"] {
		t.Fatalf("round trip lost data: %v", out)
	}
	if encodeLabels(nil) != nil {
		t.Fatal("empty labels should encode to nil, not \"null\"")
	}
}
