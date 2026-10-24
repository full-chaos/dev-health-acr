package escapes

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}

type Client interface {
	Query(statement string) (Rows, error)
}

type wrapped struct{ Rows }

func Returned(c Client) (Rows, error) {
	return c.Query("select 1")
}

func ReturnedLater(c Client) (Rows, error) {
	rows, err := c.Query("select 1")
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func Wrapped(c Client) (Rows, error) {
	rows, err := c.Query("select 1")
	if err != nil {
		return nil, err
	}
	return &wrapped{Rows: rows}, nil
}

func Stored(c Client, into *Rows) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	*into = rows
	return nil
}

func Nested(c Client) {
	consume(c.Query("select 1"))
}

func Goroutine(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	go func() { rows.Close() }()
	return nil
}

func Sent(c Client, out chan<- Rows) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	out <- rows
	return nil
}

func Aliased(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	other := rows
	_ = other
	return nil
}

func consume(Rows, error) {}

func WrappedLocal(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	local := wrapped{Rows: rows}
	_ = local
	return nil
}
