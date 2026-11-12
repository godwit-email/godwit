//go:build !darwin

package main

func responsiblePID(pid int) int { return pid }
