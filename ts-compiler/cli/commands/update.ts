import { handleInstall } from "./install.js";

export function handleUpdate() {
  console.log("Updating dependencies...");
  // In a real implementation, this would ignore the lockfile and fetch latest versions
  // For the prototype, we can just run install which behaves similarly since we don't have a real registry yet.
  handleInstall();
}
