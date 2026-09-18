<script setup lang="ts">
/** 重命名模板的变量速查表。纯文档页，无交互、无请求 */
const SYNTAX = [
  { code: '{变量名}', desc: '取变量的值' },
  { code: '<...>', desc: '尖括号包围称「块」，块内变量非空时输出块内容' },
  { code: '<{name}...>', desc: '给块取名，可用 {name} 反复引用' },
  { code: '<?{name}...>', desc: '只取名不输出，便于后续引用' },
  { code: '<{title}>', desc: '先判断 title 是否为空再取值（推荐）' },
  { code: '[[ ]]', desc: '代替 { } 输出花括号（解决语法冲突）' },
  { code: "{resource_effect.replace('.', ' ')}", desc: '替换 . 为空格' },
  { code: '{resource_effect.lower()}', desc: '转小写' },
  { code: '{resource_effect.upper()}', desc: '转大写' },
  { code: "{'2160p' if resource_pix=='4k' else resource_pix}", desc: '条件判断' },
]

const GROUPS = [
  {
    title: '基本信息',
    vars: [
      ['{title}', 'TMDB 标题', '钢铁侠'],
      ['{en_title}', '英文标题（空时转拼音）', 'Iron Man'],
      ['{original_name}', '原文件名', '钢铁侠.2008.2160p.mkv'],
      ['{year}', '上映年份', '2008'],
      ['{tmdb_id}', 'TMDB ID', '1726'],
      ['{first_letter}', '拼音首字母（大写）', 'G'],
      ['{ext}', '文件扩展名', 'mkv'],
      ['{custom_regex_match}', '自定义正则匹配', '自定义'],
    ],
  },
  {
    title: '剧集专用',
    vars: [
      ['{season_episode}', '季集 SxxExx', 'S01E01'],
      ['{season_num}', '季号', '1'],
      ['{episode_num}', '集号', '1'],
      ['{season_name}', '季名', '东海篇'],
      ['{episode_name}', '集名', '我是路飞'],
      ['{season_year}', '季年份', '1999'],
      ['{disc_num}', '盘号', '1'],
    ],
  },
  {
    title: '资源信息',
    vars: [
      ['{resource_pix}', '分辨率', '2160p'],
      ['{fps}', '帧率', '60FPS'],
      ['{resource_version}', '资源版本', 'IMAX'],
      ['{resource_source}', '资源来源', 'NF'],
      ['{resource_type}', '资源质量', 'BluRay'],
      ['{resource_effect}', '特效', 'DV.HDR'],
      ['{video_encode}', '视频编码', 'H265.10bit'],
      ['{audio_encode}', '音频编码', 'TrueHD.7.1'],
      ['{resource_team}', '发布组', 'TnT'],
    ],
  },
]
</script>

<template>
  <div class="stack">
    <section class="block">
      <h3 class="block-title">模板语法</h3>
      <div class="chips">
        <div v-for="s in SYNTAX" :key="s.code" class="chip">
          <code>{{ s.code }}</code>
          <span>{{ s.desc }}</span>
        </div>
      </div>
    </section>

    <section v-for="g in GROUPS" :key="g.title" class="block">
      <h3 class="block-title">{{ g.title }}</h3>
      <div class="chips">
        <div v-for="v in g.vars" :key="v[0]" class="chip">
          <code>{{ v[0] }}</code>
          <span>{{ v[1] }}</span>
          <em>{{ v[2] }}</em>
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
  padding: 18px;
  background: var(--c-bg-elevated);
  border: 1px solid var(--c-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-card);
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
.chip {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 6px 11px;
  border-radius: var(--radius-sm);
  background: var(--c-bg-raised);
  font-size: 12.5px;
  color: var(--c-text-2);
  transition: background-color 0.15s;
}
.chip:hover {
  background: var(--c-primary-soft);
}
.chip code {
  font-family: var(--font-mono);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--c-primary);
}
.chip em {
  font-style: normal;
  font-size: 11px;
  color: var(--c-text-4);
}
</style>
