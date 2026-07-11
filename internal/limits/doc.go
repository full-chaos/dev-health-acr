// Package limits provides process-local request controls for authenticated ACR
// principals. Its in-memory Manager does not coordinate quota or concurrency
// across replicas; cluster-wide enforcement requires a shared backend.
package limits
