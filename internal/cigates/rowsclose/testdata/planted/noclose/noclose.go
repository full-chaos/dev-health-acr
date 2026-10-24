package noclose

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}

type Client interface {
	Query(statement string) (Rows, error)
}

func Never(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := rows.Scan(); err != nil {
			return err
		}
	}
	return nil
}

func Blank(c Client) error {
	_, err := c.Query("select 1")
	return err
}

func Statement(c Client) {
	c.Query("select 1")
}

func Declared(c Client) error {
	var rows, err = c.Query("select 1")
	if err != nil {
		return err
	}
	_ = rows.Next()
	return nil
}

func DeferOther(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer rows.Next()
	return nil
}

func WrongVariable(c Client, other Rows) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer other.Close()
	_ = rows.Next()
	return nil
}
