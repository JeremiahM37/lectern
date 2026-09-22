import { strict as assert } from 'node:assert';
import { test } from 'node:test';
import { replacementBytes } from './android-input';

test('prediction replaces the word already sent, including shorter corrections', () => {
  assert.equal(replacementBytes('hello', 'jello '), '\x7f'.repeat(5) + 'jello ');
  assert.equal(replacementBytes('hellp', 'hello'), '\x7fo');
  assert.equal(replacementBytes('testing', 'test'), '\x7f'.repeat(3));
  assert.equal(replacementBytes('hello', 'hello'), '');
});
test('composition edits and deletion count graphemes rather than UTF-16 units', () => {
  assert.equal(replacementBytes('你', '你好'), '好');
  assert.equal(replacementBytes('👩‍💻', 'a'), '\x7fa');
  assert.equal(replacementBytes('e\u0301', ''), '\x7f');
});
