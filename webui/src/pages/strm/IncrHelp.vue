<script setup lang="ts">
/** 增量同步功能介绍。纯文档，没有状态——排版交给 HelpDoc */
import HelpDoc from '@/components/ui/HelpDoc.vue'
</script>

<template>
  <HelpDoc>
    <section class="block">
      <h3 class="block-title">它解决什么问题</h3>
      <p class="p">
        全量同步是整库扫描，请求量大、跑一次几分钟起步，不可能每分钟来一遍。
        但媒体库时时刻刻在变：手机上传了一部片子、离线下载落库、在 115 网页端删掉或改名了什么——
        这些变化本地并不知道，STRM 就对不上了。
      </p>
      <p class="p">
        增量同步走的是 115 的<strong>「生活事件」流</strong>（网盘操作流水）：拉回最近发生了什么，
        按事件直接定位到那几个文件，只动它们，<strong>不遍历目录树</strong>。
        所以它能几十秒跑一轮，而全量只适合偶尔跑一次。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">前提</h3>
      <ul class="ul">
        <li>115 生活 APP 里开启「最近」——事件记录关掉的话接口一直返回空，增量只会静默空转</li>
        <li>先完整跑过一次全量同步：增量只处理「变化」，底子得先有</li>
      </ul>
    </section>

    <section class="block">
      <h3 class="block-title">哪些事件会处理</h3>
      <table class="tb">
        <tr>
          <th>事件</th>
          <th>本地动作</th>
        </tr>
        <tr>
          <td>上传 / 收录 / 复制</td>
          <td>媒体库内的视频 → 生成 STRM，并按配置下载字幕、封面等附属文件</td>
        </tr>
        <tr>
          <td>新建 / 复制目录</td>
          <td>整目录转存进来的情况：把该目录加入本轮受影响集合，一并处理</td>
        </tr>
        <tr>
          <td>删除</td>
          <td>按同步台账精确定位，删掉对应的 .strm 与附属文件和台账行；目录删除 = 整棵子树清理</td>
        </tr>
        <tr>
          <td>移动 / 改名</td>
          <td>旧位置按台账清理，新位置重建；目录整体移动时本地目录直接跟着搬，不重新下载</td>
        </tr>
      </table>
      <p class="note">
        删除类动作<strong>一律以台账为准</strong>，绝不按文件名模糊匹配——同名文件误删一次就是真丢文件。
        整轮处理中途失败不会推进游标，这批事件下一轮重做。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">哪些不归它管</h3>
      <ul class="ul">
        <li>
          <strong>工作区里的变动</strong>：待整理、已存在、冗余、转存目录里的增删改一概不监控——
          那是自动整理的地盘，整理完搬进媒体库才算数
        </li>
        <li>
          <strong>整理自己做的事</strong>：整理在网盘上的每一次搬移 / 改名都登记在案，
          事件绕一圈回来时直接跳过。它落盘时已经写好 STRM 了，再处理一遍就是重复劳动
        </li>
        <li><strong>浏览、标星一类的事件</strong>：与文件内容无关，直接忽略</li>
      </ul>
    </section>

    <section class="block">
      <h3 class="block-title">跑多勤合适</h3>
      <p class="p">
        默认 <code>30</code> 秒一轮，最小 15 秒。一轮通常只发 1~2 个请求，没有新事件时完全静默，
        所以跑得勤的代价很低：30 秒一轮意味着手机上传的片子最多半分钟就能进媒体库。
        填 <code>0</code> 则关闭独立轮询，增量改为跟着「自动整理」的 cron 串行跑一次（不推荐，实时性全丢）。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">它也有够不着的地方</h3>
      <p class="p">
        生活事件有时间窗口：网页端的批量删除、服务停机期间发生的变动，都可能压根拉不回来。
        事件表本身也只保留 30 天。这类缺口只有整库差集才查得出来——
        到「全量同步」开启<strong>失效 STRM 检测</strong>，跑一次全量即可对齐（只标记不删，确认后再清理）。
      </p>
      <p class="note">
        另外：某个网盘目录持续读不出来时，相关事件会一直积压重试；超过 7 天会自动停止重试并在日志里提示，
        同样用一次全量补齐。页面上的「积压事件」数字持续不降就是这种情况。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">没同步怎么排查</h3>
      <p class="p">
        先看本页的<strong>事件流状态</strong>卡片：事件开关是否正常（对应 115 生活 APP 的「最近」）、
        上一轮跑在什么时候、有没有报错、积压多少条。再点<strong>「测试事件流」</strong>——
        它只把最近的事件读出来给你看，<strong>不推进游标、不落库、不动本地文件</strong>，随便点。
        能看到事件说明通道是通的，问题多半在作用域（不在媒体库范围内）或文件类型上。
      </p>
    </section>
  </HelpDoc>
</template>

