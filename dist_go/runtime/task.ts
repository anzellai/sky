/**
 * Sky Task runtime.
 */

export const succeed = (value: any) => ({
  $: 'Succeed',
  value
});

export const fail = (error: any) => ({
  $: 'Fail',
  error
});

export const map = (fn: (a: any) => any) => (task: any) => ({
  $: 'Map',
  fn,
  task
});

export const andThen = (fn: (a: any) => any) => (task: any) => ({
  $: 'AndThen',
  fn,
  task
});

/**
 * Executes a task and returns its result via a callback.
 */
export function runTask(task: any, callback: (result: { error?: any, value?: any }) => void) {
  switch (task.$) {
    case 'Succeed':
      callback({ value: task.value });
      break;
    case 'Fail':
      callback({ error: task.error });
      break;
    case 'Map':
      runTask(task.task, (res) => {
        if (res.error) callback(res);
        else callback({ value: task.fn(res.value) });
      });
      break;
    case 'AndThen':
      runTask(task.task, (res) => {
        if (res.error) callback(res);
        else runTask(task.fn(res.value), callback);
      });
      break;
  }
}
