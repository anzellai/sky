import React, { useState, useCallback, useEffect, useRef } from 'react';
import { runTask } from './task.js';

/**
 * Elm-style runtime for Sky UI programs in React with Effects.
 */

export interface Program<Model, Msg> {
  init: (unit: any) => [Model, any];
  update: (msg: Msg) => (model: Model) => [Model, any];
  view: (model: Model) => (dispatch: (msg: Msg) => void) => any;
  subscriptions: (model: Model) => any;
}

/**
 * Creates a React component from a Sky program definition.
 */
export function makeProgram<Model, Msg>(program: Program<Model, Msg>) {
  return function SkyApp() {
    const [initModel, initCmd] = program.init(undefined);
    const [model, setModel] = useState(initModel);
    
    // Track current model in a ref for the dispatch callback
    const modelRef = useRef(model);
    modelRef.current = model;

    const runCmd = useCallback((cmd: any, dispatch: (msg: Msg) => void) => {
      if (!cmd || cmd.$ === 'None') return;
      
      if (cmd.$ === 'Batch') {
        cmd.cmds.forEach((c: any) => runCmd(c, dispatch));
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
    }, []);

    const dispatch = useCallback((msg: Msg) => {
      const [nextModel, nextCmd] = program.update(msg)(modelRef.current);
      setModel(nextModel);
      runCmd(nextCmd, dispatch);
    }, [program, runCmd]);

    // Run init command once
    useEffect(() => {
      runCmd(initCmd, dispatch);
    }, []); // eslint-disable-line react-hooks/exhaustive-deps

    // Handle subscriptions (simplified for now)
    useEffect(() => {
      const subs = program.subscriptions(model);
      // Subscription management logic would go here
    }, [model, program]);

    return program.view(model)(dispatch);
  };
}
