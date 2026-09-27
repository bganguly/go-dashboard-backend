package service

import "fmt"

// queryArgs accumulates positional pgx-style args ($1, $2, …).
type queryArgs struct {
	args []any
}

func (q *queryArgs) Add(v any) string {
	q.args = append(q.args, v)
	return fmt.Sprintf("$%d", len(q.args))
}

func (q *queryArgs) Args() []any { return q.args }
