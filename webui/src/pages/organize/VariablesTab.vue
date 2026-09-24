<script setup lang="ts">
/**
 * 重命名模板的语法 / 变量速查。纯文档，变量表从 RENAME_VAR_GROUPS 取，
 * 不再和「插入变量」面板各维护一份。
 */
import { RENAME_VAR_GROUPS } from '@/utils/rename'

const SYNTAX = [
  { code: '{变量名}', desc: '取变量的值；认不出的变量名会原样留在文件名里' },
  { code: '<...{变量}...>', desc: '块：块内变量有值才输出整块，任一为空整块省略' },
  { code: '[[ ]]', desc: '输出真正的花括号（{ } 被变量语法占用了）' },
  { code: '/', desc: '在文件夹规则里分隔层级，可建多级目录' },
  { code: "{变量.replace('.', ' ')}", desc: '把值里的点换成空格' },
  { code: '{变量.lower()}', desc: '值转小写' },
  { code: '{变量.upper()}', desc: '值转大写' },
]
</script>

<template>
  <div class="stack">
    <section class="block">
      <h3 class="block-title">模板语法</h3>
      <div class="chips">
        <div v-for="s in SYNTAX" :key="s.code" class="var-chip">
          <code>{{ s.code }}</code>
          <span>{{ s.desc }}</span>
        </div>
      </div>
      <p class="note">
        输出会自动清理：连续的 <code>.</code> / <code>-</code> 压成一个，首尾多余的分隔符去掉。
        所以 <code>{title}.{year}</code> 在没有年份时得到的是 <code>钢铁侠</code>，不会留下尾巴。
      </p>
    </section>

    <section v-for="g in RENAME_VAR_GROUPS" :key="g.key" class="block">
      <h3 class="block-title">{{ g.title }}</h3>
      <div class="chips">
        <div v-for="v in g.vars" :key="v.token" class="var-chip">
          <code>{{ v.token }}</code>
          <span>{{ v.label }}</span>
          <em v-if="v.example">{{ v.example }}</em>
          <i v-if="v.optional" class="flag">可能为空</i>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.stack {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

.block-title {
  margin: 0 0 9px;
  padding-bottom: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--c-primary);
  border-bottom: 2px solid var(--c-primary-soft);
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.var-chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 11px;
  border-radius: var(--r-sm);
  background: var(--c-bg-raised);
  font-size: 12.5px;
  color: var(--c-text-2);
  transition: background-color 0.15s;
}
.var-chip:hover {
  background: var(--c-primary-soft);
}
.var-chip code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--c-primary);
}
.var-chip em {
  font-style: normal;
  font-size: 11px;
  color: var(--c-text-4);
}
.var-chip .flag {
  font-style: normal;
  font-size: 10.5px;
  padding: 1px 5px;
  border-radius: 999px;
  background: var(--c-bg-hover);
  color: var(--c-text-4);
}

.note {
  margin: 10px 0 0;
  font-size: 11.5px;
  line-height: 1.7;
  color: var(--c-text-3);
}
.note code {
  font-family: var(--font-mono);
  color: var(--c-primary);
}
</style>
