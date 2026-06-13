workflow {
  name          = "shell_adapter_smoke_test"
  version       = "1"
  initial_state = "run"
  target_state  = "done"
}

adapter "shell" "test" {
  source  = "ghcr.io/brokenbots/criteria-adapter-shell"
  version = "0.5.2"
}

step "hello" {
  adapter = adapter.shell.test
  input {
    command = "echo 'Hello from criteria-adapter-shell v0.5.2'"
  }
  outcome "success" { next = state.done }
  outcome "failure" { next = state.failed }
}

state "done" { terminal = true }
state "failed" {
  terminal = true
  success  = false
}
