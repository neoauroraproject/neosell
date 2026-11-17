//go:build !linux

package agent

func listeningPorts() map[int]bool { return map[int]bool{} }
