import { runTask } from './task.js';

/**
 * Elm-style runtime for Sky programs in Node.js (CLI, Servers) with Effects.
 */

export interface Program<Model, Msg> {
  init: (unit: any) => [Model, any];
  update: (msg: Msg) => (model: Model) => [Model, any];
  subscriptions: (model: Model) => any;
}

/**
 * Creates a Node.js process from a Sky program definition.
 */
export function makeProgram<Model, Msg>(program: Program<Model, Msg>) {
  let [model, initCmd] = program.init(undefined);

  function runCmd(cmd: any) {
    if (!cmd || cmd.$ === 'None') return;

    if (cmd.$ === 'Batch') {
      cmd.cmds.forEach((c: any) => runCmd(c));
      return;
    }

    if (cmd.$ === 'Perform') {
      runTask(cmd.task, (result) => {
        if (result.value !== undefined) {
          dispatch(cmd.tagger(result.value));
        }
      });
      return;
    }
  }

  function dispatch(msg: Msg) {
    const [nextModel, nextCmd] = program.update(msg)(model);
    model = nextModel;
    runCmd(nextCmd);
    
    // Refresh subscriptions
    const subs = program.subscriptions(model);
    // Subscription management logic would go here
  }

  // Start the program
  runCmd(initCmd);

  // Keep the process alive
  if (typeof process !== 'undefined' && process.stdin && process.stdin.resume) {
    process.stdin.resume();
  }

  return {
    dispatch,
    getModel: () => model
  };
}
