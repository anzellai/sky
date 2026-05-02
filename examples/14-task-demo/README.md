# Task Demo

Demonstrates the Task effect boundary -- the separation between pure functions and effectful operations (file I/O, HTTP requests, randomness).

## Build & Run

```bash
sky build src/Main.sky
./sky-out/app
```

Shows how `Task.andThen`, `Task.map`, `Task.sequence`, and `Task.parallel` compose effectful computations.

<img width="671" height="416" alt="14-task-demo" src="https://github.com/user-attachments/assets/783c5d0d-6ecb-421e-9779-5f2738005f8b" />
