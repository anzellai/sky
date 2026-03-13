/**
 * Sky Sub runtime.
 */

export const none = { $: 'None' };

export const batch = (subs: any[]) => ({
  $: 'Batch',
  subs: subs.flat()
});
