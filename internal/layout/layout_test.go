package layout

import (
	"slices"
	"testing"
)

// three is a 120×30 window: %0 on the left, %1 above %2 on the right.
const three = `{"V":2,"L":{"t":"h","w":120,"h":30,"x":0,"y":0,"c":[{"t":"p","w":60,"h":30,"x":0,"y":0,"a":true,"i":0,"I":"%0"},{"t":"v","w":59,"h":30,"x":61,"y":0,"c":[{"t":"p","w":59,"h":15,"x":61,"y":0,"l":1,"i":1,"I":"%1"},{"t":"p","w":59,"h":14,"x":61,"y":16,"l":0,"i":2,"I":"%2"}]}]}}`

func TestParseRoundTrip(t *testing.T) {
	l, err := Parse(three)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.String(); got != three {
		t.Errorf("String() =\n%s\nwant\n%s", got, three)
	}
	if got := l.Panes(); !slices.Equal(got, []string{"%0", "%1", "%2"}) {
		t.Errorf("Panes() = %v", got)
	}
}

func TestParseRefuses(t *testing.T) {
	for _, s := range []string{"", "b25d,120x30,0,0,0", `{"V":2}`} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) took it", s)
		}
	}
}

func TestWithout(t *testing.T) {
	l, _ := Parse(three)
	got, ok := l.Without("%0")
	if !ok {
		t.Fatal("nothing left")
	}
	// The h split had two children: the v split takes its place.
	if got.Root.Type != Vertical || !slices.Equal(got.Panes(), []string{"%1", "%2"}) {
		t.Errorf("Without(%%0) = %s", got)
	}
	if _, ok := l.Without("%0", "%1", "%2"); ok {
		t.Error("Without(all) left something")
	}
	same, _ := l.Without("%9")
	if same.String() != three {
		t.Errorf("Without(an id not there) = %s", same)
	}
}

func TestScale(t *testing.T) {
	l, _ := Parse(three)
	main, _ := l.Without("%0")
	s := main.Scale(80, 20)
	r := s.Root
	if r.X != 0 || r.Y != 0 || r.W != 80 || r.H != 20 {
		t.Fatalf("root = %+v", r)
	}
	a, b := r.Cells[0], r.Cells[1]
	if a.H+1+b.H != 20 || a.Y != 0 || b.Y != a.H+1 {
		t.Errorf("children %+v and %+v do not fill 20 rows", a, b)
	}
	if a.H < b.H {
		t.Errorf("the larger child got less: %d < %d", a.H, b.H)
	}
	for _, c := range r.Cells {
		if c.W != 80 || c.X != 0 {
			t.Errorf("child %+v not 80 wide at 0", c)
		}
	}
}

func TestShare(t *testing.T) {
	for _, c := range []struct {
		total   int
		weights []int
		want    []int
	}{
		{81, []int{40, 40}, []int{40, 40}},
		{80, []int{40, 40}, []int{40, 39}},
		{10, []int{1, 1, 1}, []int{3, 3, 2}},
		{5, []int{100, 1, 1}, []int{1, 1, 1}},
		{20, []int{0, 0}, []int{10, 9}},
	} {
		got := Share(c.total, c.weights)
		if !slices.Equal(got, c.want) {
			t.Errorf("Share(%d, %v) = %v, want %v", c.total, c.weights, got, c.want)
		}
		sum := len(got) - 1
		for _, s := range got {
			sum += s
			if s < 1 {
				t.Errorf("Share(%d, %v) has a size under 1: %v", c.total, c.weights, got)
			}
		}
		if sum != c.total && c.total >= 2*len(got)-1 {
			t.Errorf("Share(%d, %v) = %v adds up to %d", c.total, c.weights, got, sum)
		}
	}
}

func TestInactive(t *testing.T) {
	l, _ := Parse(three)
	if l.Inactive().Root.Cells[0].Active {
		t.Error("a pane is still active")
	}
	if !l.Root.Cells[0].Active {
		t.Error("Inactive changed the original")
	}
}

func TestEven(t *testing.T) {
	l, _ := Parse(three)
	v := l.Even("%1", "%2").Root.Cells[1]
	if v.Cells[0].H != v.Cells[1].H {
		t.Errorf("heights = %d, %d; want them even", v.Cells[0].H, v.Cells[1].H)
	}
	if same := l.Even("%0", "%1"); same.String() != three {
		t.Errorf("Even of panes in different splits changed the layout: %s", same)
	}
}
