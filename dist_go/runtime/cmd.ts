/**
 * Sky Cmd runtime.
 */

export const none = { $: 'None' };

export const batch = (cmds: any[]) => ({
  $: 'Batch',
  cmds: cmds.flat()
});

export const perform = (tagger: (a: any) => any) => (task: any) => ({
  $: 'Perform',
  tagger,
  task
});
