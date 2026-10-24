package negative

import "database/sql"

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}

type Client interface {
	Query(statement string) (Rows, error)
}

type pgxLike interface {
	Next() bool
	Scan(...any) error
	Close()
}

type pgxClient interface {
	Query(statement string) (pgxLike, error)
}

func Deferred(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(); err != nil {
			return err
		}
	}
	return nil
}

func DeferredWithoutErrCheck(c Client) {
	rows, _ := c.Query("select 1")
	defer rows.Close()
}

func DeferredClosure(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		_ = rows.Close()
	}()
	return nil
}

func PassedToAScanner(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer rows.Close()
	return scan(rows)
}

func InAnIfBranch(c Client, ok bool) error {
	if ok {
		rows, err := c.Query("select 1")
		if err != nil {
			return err
		}
		defer rows.Close()
	}
	return nil
}

func PgxLike(c pgxClient) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	_ = rows.Next()
	return nil
}

func SQLRows(db *sql.DB) error {
	rows, err := db.Query("select 1")
	if err != nil {
		return err
	}
	_ = rows.Next()
	return nil
}

func InACase(c Client, n int) error {
	switch n {
	case 1:
		rows, err := c.Query("select 1")
		if err != nil {
			return err
		}
		defer rows.Close()
	}
	return nil
}

func scan(r Rows) error { return r.Scan() }

func ClosureWithIfInit(c Client) (err error) {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	return nil
}

func ClosureWithANestedFunc(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		later := func() int { return 1 }
		_ = later
		rows.Close()
	}()
	return nil
}
