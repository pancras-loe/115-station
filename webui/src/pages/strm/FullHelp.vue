<script setup lang="ts">
/** 全量同步 + 失效 STRM 检测的功能介绍。纯文档，排版交给 HelpDoc */
import HelpDoc from '@/components/ui/HelpDoc.vue'
</script>

<template>
  <HelpDoc>
    <section class="block">
      <h3 class="block-title">它做什么</h3>
      <p class="p">
        把 115 媒体库目录整棵树遍历一遍：
        视频文件按「STRM 配置」的域名与格式生成 <code>.strm</code>，
        字幕、封面、<code>.nfo</code> 等附属文件按后缀配置下载到本地，
        每个产物都记一条<strong>台账</strong>（本地相对路径 ↔ 115 文件 id）。
      </p>
      <p class="p">
        整理的工作区目录（待整理 / 已存在 / 冗余 / 转存）会被排除在外，不会被当成媒体同步。
        本地已经有的 STRM 是覆盖还是跳过，取决于「STRM 配置」里的设置。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">什么时候需要跑</h3>
      <ul class="ul">
        <li><strong>第一次用</strong>：本地空的，增量只处理「变化」，底子得靠全量铺出来</li>
        <li><strong>换了媒体库目录或本地目录</strong>、改了直链格式 / 后缀配置</li>
        <li><strong>怀疑本地和网盘对不上</strong>：Emby 里点开是 404，或者少了一批片子</li>
        <li><strong>刷新失效标记</strong>：见下面「失效 STRM 检测」</li>
      </ul>
      <p class="note">
        日常不需要跑全量。新增内容有增量同步（30 秒一轮）和自动整理各自落盘，
        整库扫描请求量大、115 风控敏感，跑多了没好处。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">两种模式</h3>
      <table class="tb">
        <tr>
          <th>模式</th>
          <th>说明</th>
        </tr>
        <tr>
          <td>标准</td>
          <td>
            逐个目录遍历，兼容性最好，走的是通用接口。请求数多、受 API 间隔限制，
            大库一次要跑几分钟到几十分钟
          </td>
        </tr>
        <tr>
          <td>快速</td>
          <td>
            一次性取回整棵目录树与文件表，请求数少两个数量级。依赖 115 客户端端点
            （只有 Cookie 通道可用，所以没绑 Cookie 时这个选项不出现），
            接口变动或取不到时<strong>自动降级为标准模式</strong>把这一趟跑完
          </td>
        </tr>
      </table>
      <p class="note">
        文件数超过 20 万的库，建议先用标准模式完整跑通一次，确认结果无异常后再切快速模式。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">跑完之后</h3>
      <ul class="ul">
        <li>有新产物时自动通知 Emby 刷新媒体库</li>
        <li>
          把当前的生活事件窗口<strong>标记为已覆盖</strong>：全量已经看过网盘的现状了，
          增量只从此刻之后的新事件接着处理，不会把同样的变化再做一遍
        </li>
        <li>开了失效检测的话，顺带刷新一次失效标记</li>
      </ul>
    </section>

    <section class="block">
      <h3 class="block-title">失效 STRM 检测</h3>
      <p class="p">
        <strong>失效 STRM</strong> = 本地还留着 .strm / 附属文件，但网盘上的源文件已经没了。
        增量同步靠生活事件感知删除，而事件有窗口：网页端的批量删除、服务停机期间的删除都可能漏掉，
        漏掉的就一直烂在库里 —— Emby 还显示着条目，点开播放 404。
      </p>
      <p class="p">
        全量同步本来就会拿到网盘当前的完整文件清单，和台账做一次<strong>差集</strong>就知道谁已经失效。
        所以这个功能挂在全量上：<strong>每跑一次全量，标记刷新一次</strong>。
      </p>
      <p class="p">
        但它<strong>只标记，不删除</strong>。两种情况下算出来的差集并不可信：
      </p>
      <ul class="ul">
        <li>
          <strong>清单没取全</strong>（翻页短缺、目录表缺项）——这时候直接删就是真丢数据，
          所以本次扫描不完整时会整个跳过标记，页面上也会提示
        </li>
        <li>
          <strong>你自己改了配置</strong>——比如从后缀列表里去掉了 jpg，
          原先同步过的图片就会变成「失效」。这是预期行为，但得让你看见再决定
        </li>
      </ul>
      <p class="note">
        清理时删除的是<strong>本地文件 + 对应台账记录</strong>，以及因此变空的目录，<strong>网盘不受影响</strong>。
        失效条目占台账比例超过 20% 时页面会红色警告——这种量级通常意味着上次扫描没取全，
        或者同步的根本不是平时那个媒体库，先重跑一次全量确认再说。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">定时全量同步</h3>
      <p class="p">
        它<strong>只为刷新失效标记而存在</strong>：新增内容有增量和整理各自落盘，不需要靠定时全量捡；
        而删除这件事只有整库差集查得出来。所以这个开关挂在失效检测底下，
        <strong>检测关掉时它整个不显示，后台也不会执行</strong>——否则就成了每天白跑一趟整库扫描，
        你在界面上还看不出来。
      </p>
      <p class="p">
        建议每天最多一次、放在夜间。定时跑同样<strong>只标记不删除</strong>：
        定时任务没人盯着，误判一次就是真丢文件，清理永远要你在页面上确认。
      </p>
      <p class="note">
        全量、增量、自动整理共用一把任务锁，同一时刻只跑一个。
        全量的 cron 和自动整理的 cron 撞在同一分钟时优先跑全量——
        整库扫描本来就覆盖了增量那点事件，跑完还会把事件窗口标记为已覆盖。
      </p>
    </section>
  </HelpDoc>
</template>
