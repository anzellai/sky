# Todo CLI

Command-line todo app with SQLite persistence via `Std.Db`.

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app list
```

Requires `sky install` first. Usage:

```bash
./sky-out/app add "Buy groceries"
./sky-out/app list
./sky-out/app done 1
./sky-out/app remove 1
./sky-out/app clear
```

<img width="790" height="912" alt="07-todo-cli" src="https://github.com/user-attachments/assets/82708b12-2ead-4d96-bbaf-f21935a2259b" />
