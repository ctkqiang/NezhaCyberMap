/*
run.mjs —— 前端逻辑测试运行器（零依赖）。

断言辅助 + 测试套件加载。用法：node web/test/run.mjs
*/

import { testProjection } from './projection.test.mjs';
import { testLand } from './land.test.mjs';
import { testRenderer } from './renderer.test.mjs';

let passed = 0;
let failed = 0;

/*
ok 断言并记录结果。

参数:
  - cond (boolean): 断言条件。
  - label (string): 用例描述。
*/
export function ok(cond, label) {
  if (cond) {
    passed++;
    console.log('  ✓ ' + label);
  } else {
    failed++;
    console.error('  ✗ ' + label);
  }
}

// 运行全部套件。
console.log('投影与点阵测试');
await testProjection(ok);
await testLand(ok);
console.log('');
console.log('渲染管线测试');
await testRenderer(ok);

console.log('');
if (failed === 0) {
  console.log('全部通过: ' + passed + ' 项');
  process.exit(0);
} else {
  console.error('失败: ' + failed + ' / ' + (passed + failed));
  process.exit(1);
}
