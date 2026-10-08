package adapters

import "testing"

func TestTicketLabel(t *testing.T) {
	blank, subj, num, spaces := "", "Fila", "7", "  "
	for _, c := range []struct {
		subject, external *string
		want              string
	}{
		{&subj, &num, "Fila"}, {&blank, &num, "nº 7"}, {nil, &num, "nº 7"}, {&spaces, &spaces, "sem assunto registrado"}, {nil, nil, "sem assunto registrado"},
	} {
		if got := ticketLabel(c.subject, c.external); got != c.want {
			t.Fatalf("ticketLabel(%v,%v)=%q want %q", c.subject, c.external, got, c.want)
		}
	}
}
