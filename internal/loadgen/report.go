package loadgen

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// WriteTable renders a sweep as an aligned text table, one row per step.
func WriteTable(w io.Writer, steps []Summary) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "offered/s\tachieved/s\terr%\tdropped\t404s\tp50\tp90\tp99\tp99.9\tmax\t")
	for _, s := range steps {
		fmt.Fprintf(tw, "%.0f\t%.1f\t%.2f\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t\n",
			s.OfferedRPS, s.AchievedRPS, 100*s.ErrorRate, s.Dropped, s.NotFound,
			round(s.P50), round(s.P90), round(s.P99), round(s.P999), round(s.Max))
	}
	return tw.Flush()
}

func round(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(10 * time.Microsecond)
	default:
		return d.Round(time.Microsecond)
	}
}
