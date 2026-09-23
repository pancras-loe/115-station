<script setup lang="ts">
/** 「识别规则」的功能介绍。纯文档，排版交给 HelpDoc */
import HelpDoc from '@/components/ui/HelpDoc.vue'
</script>

<template>
  <HelpDoc>
    <section class="block">
      <h3 class="block-title">它管的是哪一段</h3>
      <p class="p">
        整理是一条流水线：<strong>扫待整理目录 → 识别 → 分类 → 洗版 → 重命名 → 搬进媒体库</strong>。
        这一页只管最前面那一小段——<strong>文件名送去识别之前</strong>的预处理与过滤。
      </p>
      <p class="p">
        识别本身是拿文件名去 TMDB 搜片名。名字里混着发布站广告、域名水印、
        「国语中字」这类标注时，搜出来的就是空——整理结果只能进冗余目录。
        这一页的作用就是先把这些噪音清掉，让 TMDB 搜得中。
      </p>
      <p class="note">
        替换只作用在「送去识别的那份名字」（以及每一集的季集号解析）上，<strong>不会改网盘里的文件名</strong>。
        入库后叫什么由「重命名策略」决定，画质、编码、发布组这些也仍旧从<strong>原始文件名</strong>提取。
      </p>
      <p class="p">
        识别不只看文件名：文件名里缺的片名、年份、季号，会<strong>由近及远</strong>从所在的子目录、
        顶层目录、再外层的容器目录上补。<code>狂飙 (2023)/Season 2/E05.mkv</code> 能认出
        「狂飙 2023 第二季第 5 集」；<code>Movies</code>、<code>合集</code> 这类分类目录名不会被当成片名。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">替换规则</h3>
      <p class="p">
        一条规则就是「把 A 换成 B」，B 留空表示<strong>删掉 A</strong>。
        多条规则<strong>从上往下依次套用</strong>，后一条作用在前一条的结果上——
        所以「收敛连续的点」这种收尾规则要放在最后。
      </p>
      <table class="tb">
        <tr>
          <th style="width: 76px">模式</th>
          <th>说明</th>
        </tr>
        <tr>
          <td>文本</td>
          <td>一模一样的字符串才替换。适合固定的词，如 <code>4K修复版</code> → <code>4K</code></td>
        </tr>
        <tr>
          <td>正则</td>
          <td>
            按正则匹配，能处理「中间内容不固定」的情况，如 <code>【[^】]*】</code> 匹配任意【】块。
            替换内容里可以用 <code>$1</code> 引用第一个捕获组
          </td>
        </tr>
      </table>
      <p class="p">
        不确定写什么就点<strong>「常用规则」</strong>：几条最常遇到的脏名字都在里面，勾上即可，
        之后还能照着改。下面的<strong>效果预览</strong>可以随时拿一个真实文件名试，命中哪几条、
        改成什么样当场就能看到，不用先保存再跑一遍整理。
      </p>
      <p class="note">
        后端用的是 Go 的 RE2 正则：<strong>不支持先行断言 <code>(?=)</code>、后行断言 <code>(?&lt;=)</code>
        和反向引用 <code>\1</code></strong>。写了这些的规则会在整理时被整条跳过（界面上会当场提示），
        其余规则照常生效。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">直接指定条目、季号、集数偏移</h3>
      <p class="p">
        替换内容里可以写一个 <code>{[…]}</code> 标签，识别时会被摘出来单独处理，
        不会混进片名去搜。各项用分号隔开，<strong>都可以单独写</strong>：
      </p>
      <table class="tb">
        <tr>
          <th style="width: 110px">写法</th>
          <th>作用</th>
        </tr>
        <tr>
          <td><code>tmdbid=69851</code></td>
          <td>直接用这个 TMDB 条目，不再按片名搜。电影和剧集的编号是两套，最好同时写 <code>type</code></td>
        </tr>
        <tr>
          <td><code>type=tv</code></td>
          <td>条目类型：<code>tv</code> 剧集 / <code>movie</code> 电影</td>
        </tr>
        <tr>
          <td><code>s=2</code></td>
          <td>强制季号。适合文件名里没写季、或者写错季的资源</td>
        </tr>
        <tr>
          <td><code>eo=-12</code></td>
          <td>
            集数偏移。番剧常见跨季连续编号：第二季从第 13 集编起，
            写 <code>eo=-12</code> 就把 13 改回第 1 集
          </td>
        </tr>
      </table>
      <p class="p">
        例：模式选「文本」，把 <code>某番</code> 替换成 <code>某番 {[tmdbid=12345;type=tv;s=2;eo=-12]}</code>，
        这部番以后每一集都会进第二季、集号减 12。
      </p>
      <p class="note">
        文件夹名本身写 <code>[tmdbid=123]</code>（Emby）、<code>[tmdbid-123]</code>（Jellyfin）、
        <code>{tmdb-123}</code>（Plex）也会被认出来，不用配规则。
      </p>
    </section>
    <section class="block">
      <h3 class="block-title">识别记忆</h3>
      <p class="p">
        在「待确认」里<strong>改了指定</strong>、或在整理记录里<strong>重新整理并选了别的条目</strong>，
        系统会记下「这个片名 + 年份 → 这个条目」。以后同名同年的内容（连载的新一集、换了发布组的资源）
        直接用你的结论，不再重新搜。
      </p>
      <p class="note">
        记错了就再改一次：重新整理选对的条目会覆盖原来的记忆。
        同名但年份不同的片子各记各的；文件名没写年份、而同名记忆又不止一条时不会乱套用。
      </p>
    </section>
    <section class="block">
      <h3 class="block-title">发布组</h3>
      <p class="p">
        系统默认按「文件名末尾 <code>-GROUP</code>」认发布组，
        <code>[WiKi] Some.Show.S01E01.mkv</code> 这种写在前面的就认不出来。
        在这里把组名填进去，名字里任何位置出现它都算命中。
      </p>
      <p class="p">
        发布组用在两处：重命名模板的 <code>{resource_team}</code> 变量，
        以及洗版规则里按 <code>resource_team</code> 判断保留哪个版本。
        不做洗版、模板里也没用到这个变量的话，这项可以空着。
      </p>
      <p class="note">
        匹配要求是<strong>完整片段</strong>：填 <code>CR</code> 不会把 <code>CRUNCHYROLL</code> 里的两个字母认成发布组。
      </p>
    </section>

    <section class="block">
      <h3 class="block-title">最小视频大小</h3>
      <p class="p">
        小于这个体积的视频<strong>不识别、不入库，直接移到冗余目录</strong>。
        挡的是资源包里混进来的预告片、引流视频、封面动图——它们一旦被识别成正片，
        就会顶着正片的名字进媒体库。
      </p>
      <p class="p">
        填 <code>0</code> 表示不限制。常见取值 50~200 MB；
        库里有短片、MV 或单集很小的番剧就往小了填，别把正片误杀。
      </p>
    </section>
  </HelpDoc>
</template>
