package earlyreturn

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}

type Client interface {
	Query(statement string) (Rows, error)
}

func ReturnBetween(c Client, ok bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer rows.Close()
	return nil
}

func DeferOnOneBranch(c Client, ok bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	if ok {
		defer rows.Close()
	}
	return nil
}

func StatementBetween(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	_ = 1
	defer rows.Close()
	return nil
}

func StaleErrorCheck(c Client, err error) error {
	rows, _ := c.Query(err.Error())
	if err != nil {
		return err
	}
	defer rows.Close()
	return nil
}
