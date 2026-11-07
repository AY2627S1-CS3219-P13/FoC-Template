package schedule

import "time"

type Config struct {
	DatabaseURL, Origin, SessionCookie                                         string
	UserServiceURL, UserServiceToken, SupplierServiceURL, SupplierServiceToken string
	CreditServiceURL, CreditServiceToken                                       string
	DependencyTimeout                                                          time.Duration
}
