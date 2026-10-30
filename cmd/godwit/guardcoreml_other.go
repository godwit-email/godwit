//go:build !darwin

package main

// guardCoreML: only macOS has CoreML; 识别模型 reads on the CPU.
func guardCoreML() string { return "" }
