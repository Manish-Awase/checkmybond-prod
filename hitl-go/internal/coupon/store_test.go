package coupon

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func ptrF(v float64) *float64 { return &v }
func ptrI(v int) *int         { return &v }

func TestNormalizeCode(t *testing.T) {
	cases := map[string]string{
		"  summer 24 ": "SUMMER24",
		"save-10":      "SAVE-10",
		"AbC":          "ABC",
		"":             "",
		"  a b  c ":    "ABC",
	}
	for in, want := range cases {
		if got := NormalizeCode(in); got != want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeDiscountPercent(t *testing.T) {
	c := &Coupon{Kind: KindPercent, Value: 25}
	d, tok := ComputeDiscount(c, 100)
	if d != 25 || tok != 0 {
		t.Fatalf("got (%v, %v), want (25, 0)", d, tok)
	}
}

func TestComputeDiscountPercentRespectsCap(t *testing.T) {
	c := &Coupon{Kind: KindPercent, Value: 50, MaxDiscount: ptrF(20)}
	d, _ := ComputeDiscount(c, 500)
	if d != 20 {
		t.Fatalf("cap ignored: got %v, want 20", d)
	}
}

func TestComputeDiscountNeverExceedsOrder(t *testing.T) {
	// A $50-off coupon on a $10 order must take it to zero, not to -$40.
	c := &Coupon{Kind: KindFlat, Value: 50}
	d, _ := ComputeDiscount(c, 10)
	if d != 10 {
		t.Fatalf("got %v, want 10 (clamped to order total)", d)
	}
}

func TestComputeDiscountHundredPercent(t *testing.T) {
	c := &Coupon{Kind: KindPercent, Value: 100}
	d, _ := ComputeDiscount(c, 49.99)
	if d != 49.99 {
		t.Fatalf("got %v, want 49.99", d)
	}
}

func TestComputeDiscountRoundsToCents(t *testing.T) {
	// 33% of 10.00 is 3.3000000000000003 in float64.
	c := &Coupon{Kind: KindPercent, Value: 33}
	d, _ := ComputeDiscount(c, 10)
	if d != 3.3 {
		t.Fatalf("got %v, want 3.3", d)
	}
}

func TestComputeDiscountTokens(t *testing.T) {
	c := &Coupon{Kind: KindTokens, Value: 250}
	d, tok := ComputeDiscount(c, 0)
	if d != 0 || tok != 250 {
		t.Fatalf("got (%v, %v), want (0, 250)", d, tok)
	}
}

func TestValidateRejectsBadDefinitions(t *testing.T) {
	cases := []struct {
		name string
		c    Coupon
	}{
		{"no code", Coupon{Kind: KindPercent, Value: 10}},
		{"unknown kind", Coupon{Code: "X1ABCD", Kind: "magic", Value: 10}},
		{"zero value", Coupon{Code: "X1ABCD", Kind: KindPercent, Value: 0}},
		{"negative value", Coupon{Code: "X1ABCD", Kind: KindFlat, Value: -5}},
		{"over 100 percent", Coupon{Code: "X1ABCD", Kind: KindPercent, Value: 150}},
		{"fractional tokens", Coupon{Code: "X1ABCD", Kind: KindTokens, Value: 10.5}},
		{"zero max redemptions", Coupon{Code: "X1ABCD", Kind: KindPercent, Value: 10, MaxRedemptions: ptrI(0)}},
		{"negative minimum", Coupon{Code: "X1ABCD", Kind: KindPercent, Value: 10, MinAmount: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.c.Validate(); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestValidateRejectsExpiryBeforeStart(t *testing.T) {
	start := time.Now()
	past := start.Add(-time.Hour)
	c := Coupon{Code: "X1ABCD", Kind: KindPercent, Value: 10, StartsAt: start, ExpiresAt: &past}
	if err := c.Validate(); err == nil {
		t.Fatal("expected an error for expiry before start")
	}
}

func TestValidateAppliesDefaults(t *testing.T) {
	c := Coupon{Code: " save10 ", Kind: KindPercent, Value: 10}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Code != "SAVE10" {
		t.Errorf("code not normalized: %q", c.Code)
	}
	if c.Currency != "USD" {
		t.Errorf("currency default = %q, want USD", c.Currency)
	}
	if c.MaxPerUser != 1 {
		t.Errorf("per-user default = %d, want 1", c.MaxPerUser)
	}
}

func TestCheckUsable(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)

	cases := []struct {
		name string
		c    Coupon
		want error
	}{
		{"active", Coupon{IsActive: true, StartsAt: past}, nil},
		{"switched off", Coupon{IsActive: false, StartsAt: past}, ErrInactive},
		{"not started", Coupon{IsActive: true, StartsAt: future}, ErrNotStarted},
		{"expired", Coupon{IsActive: true, StartsAt: past, ExpiresAt: &past}, ErrExpired},
		{"exhausted", Coupon{IsActive: true, StartsAt: past, MaxRedemptions: ptrI(5), RedeemedCount: 5}, ErrExhausted},
		{"has room", Coupon{IsActive: true, StartsAt: past, MaxRedemptions: ptrI(5), RedeemedCount: 4}, nil},
		{"unlimited", Coupon{IsActive: true, StartsAt: past, RedeemedCount: 99999}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkUsable(&tc.c, now)
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCheckOrder(t *testing.T) {
	c := Coupon{AppliesToPlans: []string{"monthly", "annual"}, MinAmount: 20}

	if err := checkOrder(&c, "per-field", 100); !errors.Is(err, ErrPlanNotAllowed) {
		t.Fatalf("plan restriction not enforced: %v", err)
	}
	if err := checkOrder(&c, "monthly", 10); !errors.Is(err, ErrBelowMinimum) {
		t.Fatalf("minimum not enforced: %v", err)
	}
	if err := checkOrder(&c, "monthly", 50); err != nil {
		t.Fatalf("valid order rejected: %v", err)
	}
	// Case-insensitive plan matching, since the plan id arrives from a URL.
	if err := checkOrder(&c, "MONTHLY", 50); err != nil {
		t.Fatalf("plan match should be case-insensitive: %v", err)
	}
	// Empty plan list means every plan.
	open := Coupon{}
	if err := checkOrder(&open, "anything", 0); err != nil {
		t.Fatalf("unrestricted coupon rejected: %v", err)
	}
}

func TestGenerateCodeShapeAndAlphabet(t *testing.T) {
	code, err := GenerateCode("SUMMER", 8)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "SUMMER-") {
		t.Fatalf("prefix missing: %q", code)
	}
	suffix := strings.TrimPrefix(code, "SUMMER-")
	if len(suffix) != 8 {
		t.Fatalf("suffix length = %d, want 8", len(suffix))
	}
	for _, r := range suffix {
		if !strings.ContainsRune(codeAlphabet, r) {
			t.Fatalf("character %q is outside the safe alphabet", r)
		}
	}
	// The ambiguous characters must never appear.
	if strings.ContainsAny(suffix, "O0I1LU") {
		t.Fatalf("ambiguous character in %q", suffix)
	}
}

func TestGenerateCodeNoPrefix(t *testing.T) {
	code, err := GenerateCode("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(code, "-") || len(code) != 10 {
		t.Fatalf("got %q, want a bare 10-character code", code)
	}
}

func TestGenerateCodeIsNotPredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := GenerateCode("", 8)
		if err != nil {
			t.Fatal(err)
		}
		if seen[c] {
			t.Fatalf("duplicate code %q within 2000 draws — generator is not random enough", c)
		}
		seen[c] = true
	}
}

func TestReasonLabelsAreBounded(t *testing.T) {
	all := []error{
		nil, ErrNotFound, ErrInactive, ErrExpired, ErrNotStarted, ErrExhausted,
		ErrAlreadyUsed, ErrPlanNotAllowed, ErrBelowMinimum, ErrWrongKind,
		errors.New("some unexpected database failure"),
	}
	seen := map[string]bool{}
	for _, err := range all {
		r := Reason(err)
		if r == "" || strings.Contains(r, " ") {
			t.Fatalf("Reason(%v) = %q — not a usable metric label", err, r)
		}
		seen[r] = true
	}
	if len(seen) != len(all) {
		t.Fatalf("expected %d distinct labels, got %d", len(all), len(seen))
	}
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		c    Coupon
		want string
	}{
		{Coupon{Kind: KindPercent, Value: 25}, "25% off"},
		{Coupon{Kind: KindPercent, Value: 50, Currency: "USD", MaxDiscount: ptrF(20)}, "50% off, up to $20.00"},
		{Coupon{Kind: KindFlat, Value: 10, Currency: "USD"}, "$10.00 off"},
		{Coupon{Kind: KindFlat, Value: 500, Currency: "INR"}, "₹500.00 off"},
		{Coupon{Kind: KindTokens, Value: 100}, "100 free fields"},
	}
	for _, tc := range cases {
		if got := Describe(&tc.c); got != tc.want {
			t.Errorf("Describe(%+v) = %q, want %q", tc.c, got, tc.want)
		}
	}
}

func TestRoundCents(t *testing.T) {
	cases := map[float64]float64{
		3.3000000000000003: 3.3,
		0.1 + 0.2:          0.3,
		10.005:             10.01,
		0:                  0,
	}
	for in, want := range cases {
		if got := roundCents(in); got != want {
			t.Errorf("roundCents(%v) = %v, want %v", in, got, want)
		}
	}
}
