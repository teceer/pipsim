package economy

import "testing"

// The rule that keeps the economy alive. Money leaves circulation permanently
// — a pip dying with wages in its pocket has them escheated to the treasury —
// so the treasury has to push money back out or the workplaces starve.
func TestEndowmentFor(t *testing.T) {
	const capital = 10_000

	cases := []struct {
		name string
		held int64
		want int64
	}{
		{"an empty workplace is filled", 0, capital},
		{
			// The bug this replaced: the old rule skipped anything holding more
			// than nothing, so a workplace drained to 15 was left there forever
			// and payroll rejected for insufficient funds on every cycle.
			name: "a nearly drained workplace is refilled, not skipped",
			held: 15,
			want: capital - 15,
		},
		{"below the half mark, topped up to capital", 4_999, capital - 4_999},
		{"at the half mark, left alone", 5_000, 0},
		{"comfortably funded, left alone", 9_000, 0},
		{"already at capital, left alone", capital, 0},
		// Nothing forbids an overdrawn workplace; the top-up has to land it at
		// capital rather than at capital-minus-the-debt.
		{"an overdrawn workplace is brought back to capital", -500, capital + 500},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := endowmentFor(c.held, capital); got != c.want {
				t.Errorf("endowmentFor(%d, %d) = %d, want %d",
					c.held, capital, got, c.want)
			}
		})
	}
}

// Whatever it issues, the workplace ends up holding exactly `capital` — the
// property that keeps the supply from creeping up one pass at a time.
func TestEndowmentNeverOvershoots(t *testing.T) {
	const capital = 10_000
	for held := int64(-1_000); held < capital; held += 137 {
		issued := endowmentFor(held, capital)
		if issued == 0 {
			continue
		}
		if after := held + issued; after != capital {
			t.Fatalf("holding %d, issued %d, ends at %d — want %d",
				held, issued, after, capital)
		}
	}
}
