import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { visibleSlice } from './viewport';

test('a slice keeps the frame that is fully visible at its natural height', () => {
  assert.deepEqual(
    visibleSlice({ top: 40, height: 800 }, { top: 0, height: 900 }),
    { top: 0, height: 800 },
  );
});

test('a keyboard overlaying the bottom shortens the slice and keeps the top', () => {
  // The phone keyboard leaves the top 500 css pixels of the layout viewport.
  assert.deepEqual(
    visibleSlice({ top: 40, height: 860 }, { top: 0, height: 500 }),
    { top: 0, height: 460 },
  );
});

test('a frame pushed above the visible window keeps its own offset', () => {
  // The top-level page panned down: the frame starts above what is visible.
  assert.deepEqual(
    visibleSlice({ top: 0, height: 800 }, { top: 60, height: 600 }),
    { top: 60, height: 600 },
  );
});

test('a frame below the visible window has no usable height', () => {
  assert.deepEqual(
    visibleSlice({ top: 900, height: 400 }, { top: 0, height: 500 }),
    { top: 0, height: 0 },
  );
});
