package notdeferred

import "runtime"

type Rows interface {
	Next() bool
	Scan(...any) error
	Close() error
}

type Client interface {
	Query(statement string) (Rows, error)
}

func ClosedAtTheEnd(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	for rows.Next() {
		if err := rows.Scan(); err != nil {
			rows.Close()
			return err
		}
	}
	return rows.Close()
}

func ConditionalInTheClosure(c Client, skip bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		if !skip {
			rows.Close()
		}
	}()
	return nil
}

func ReturnBeforeCloseInTheClosure(c Client, skip bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		if skip {
			return
		}
		rows.Close()
	}()
	return nil
}

func PanicBeforeCloseInTheClosure(c Client, skip bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		if skip {
			panic("skip")
		}
		rows.Close()
	}()
	return nil
}

func GotoBeforeCloseInTheClosure(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		goto end
	end:
		_ = 0
		rows.Close()
	}()
	return nil
}

func GoexitBeforeCloseInTheClosure(c Client, skip bool) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		if skip {
			runtime.Goexit()
		}
		rows.Close()
	}()
	return nil
}

func InvokedLiteralPanicsBeforeClose(c Client) error {
	rows, err := c.Query("select 1")
	if err != nil {
		return err
	}
	defer func() {
		func() { panic("before close") }()
		rows.Close()
	}()
	return nil
}
