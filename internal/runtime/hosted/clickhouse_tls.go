package hosted

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
)

const maximumClickHouseCABytes = 1 << 20

func clickHouseTLSConfig(path string) (configuration *tls.Config, resultErr error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("ClickHouse CA bundle is invalid")
	}
	defer func() {
		if err := file.Close(); err != nil {
			configuration = nil
			resultErr = errors.Join(resultErr, errors.New("ClickHouse CA bundle is invalid"))
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maximumClickHouseCABytes {
		return nil, errors.New("ClickHouse CA bundle is invalid")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumClickHouseCABytes+1))
	if err != nil || len(contents) > maximumClickHouseCABytes {
		return nil, errors.New("ClickHouse CA bundle is invalid")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(contents) {
		return nil, errors.New("ClickHouse CA bundle is invalid")
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, nil
}
