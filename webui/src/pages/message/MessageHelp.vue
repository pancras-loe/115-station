<script setup lang="ts">
/**
 * 消息通知各通道的使用说明。纯文档，排版交给 HelpDoc。
 *
 * 写的都是本站实际的行为：企微回调走 302 直链端口（proxy.go 注册的 /wecom/callback），
 * TG 是本站主动长轮询、不需要公网地址，OneBot 指令与企微同一套路由、回复按通知广播。
 * 后端改了这些，这里要跟着改。
 */
import HelpDoc from '@/components/ui/HelpDoc.vue'

defineProps<{ channel: 'wecom' | 'tg' | 'feishu' | 'onebot' | 'qqoff' }>()

const origin = location.origin
</script>

<template>
  <HelpDoc>
    <!-- ==================== 企业微信 ==================== -->
    <template v-if="channel === 'wecom'">
      <section class="block">
        <h3 class="block-title">能做什么</h3>
        <ul class="ul">
          <li><strong>通知推送</strong>：入库、整理失败、订阅补上了、离线下载失败等消息推到企业微信（手机上装「企业微信」或在微信里关注「微信插件」都能收）</li>
          <li><strong>双向机器人</strong>（可选）：在应用会话里直接发链接转存、搜资源、订阅、触发整理 / 同步，底栏还有现成的菜单按钮</li>
        </ul>
        <p class="note">只想收通知的话做完第 1、2、4 步即可；第 3 步「接收消息」是给机器人互动用的。</p>
      </section>

      <section class="block">
        <h3 class="block-title">第 1 步：建一个自建应用</h3>
        <ul class="ul">
          <li>电脑浏览器登录 <code>work.weixin.qq.com</code> 管理后台（没有企业就自己注册一个，个人也能建，不用认证）</li>
          <li>「我的企业」页最下面的<strong>企业 ID</strong>（ww 开头）→ 填到本页「企业 ID」</li>
          <li>「应用管理 → 自建 → 创建应用」，名字头像随意。<strong>可见范围选你自己</strong>：本站的通知发给可见范围内所有人</li>
          <li>建好后在应用详情页拿到 <strong>AgentId</strong> 和 <strong>Secret</strong>（Secret 要点「查看」，发到企业微信里查收）→ 填到本页</li>
        </ul>
      </section>

      <section class="block">
        <h3 class="block-title">第 2 步：企业可信 IP（必做）</h3>
        <p class="p">
          应用详情 →「开发者接口 → 企业可信IP」，填<strong>本站服务器的出口公网 IP</strong>
          （在服务器上打开 ip.sb 之类的网站看到的那个）。不填的话发消息会报
          <code>60020 not allow to access from your ip</code>，测试通知必然失败。
        </p>
        <p class="p">
          如果后台提示要先配置「可信域名」或「接收消息服务器」才能填可信 IP，就先做第 3 步再回来。
        </p>
        <p class="note">
          家宽动态 IP、或服务器在海外连官方接口很慢：在一台固定 IP 的国内服务器上反代
          <code>https://qyapi.weixin.qq.com</code>，把那台机器的 IP 加进可信 IP，再把本页「API 地址」改成反代地址。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">第 3 步：接收消息（机器人互动才需要）</h3>
        <ul class="ul">
          <li>应用详情 →「接收消息 → 设置 API 接收」</li>
          <li>
            URL 填 <code>http://&lt;公网地址&gt;:6086/wecom/callback</code>。
            注意是 <strong>6086（302 直链端口）</strong>，不是管理后台的 6060；套了反代或改过端口映射就按你的实际地址填
          </li>
          <li>Token 和 EncodingAESKey 都点「随机获取」，复制到本页对应的两栏</li>
          <li>
            <strong>先在本页启用并保存</strong>，再回企微点保存：企微保存时会立刻请求一次这个 URL 做验证，
            本站还没保存上这两个值就会验证失败
          </li>
        </ul>
      </section>

      <section class="block">
        <h3 class="block-title">第 4 步：启用、保存、测试</h3>
        <p class="p">
          状态选「启用」→ 保存配置 → 点「测试通知」，到企业微信里应用的会话查收。
          保存时本站会顺手给应用生成底栏菜单（自动整理 / 增量同步），不用自己去后台配。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">机器人指令</h3>
        <table class="tb">
          <thead><tr><th>发什么</th><th>做什么</th></tr></thead>
          <tbody>
            <tr><td>直接发链接</td><td>磁力 / ed2k / HTTP 提交 115 离线下载；115 分享链接（连同提取码一起发）转存后自动整理入库；阿里云盘分享走秒传</td></tr>
            <tr><td><code>搜索 片名</code></td><td>找资源：观影 / 盘搜 / TG 频道 / 不太灵 / RE0 一起搜，选好影片后回复序号转存或离线；可以连续挑几条</td></tr>
            <tr><td><code>观影 片名</code> / <code>网盘 片名</code></td><td>只搜一个来源</td></tr>
            <tr><td>资源列表里</td><td><code>0</code> 自动择优、<code>n</code> / <code>p</code> 翻页、<code>b</code> 重新选片、<code>r</code> 重搜、<code>s</code> 订阅这部、<code>q</code> 退出；RE0 要花积分的会先问，回 <code>y</code> 确认、<code>c</code> 取消</td></tr>
            <tr><td><code>订阅 片名</code></td><td>订阅：定时找缺的集，转存后自动入库</td></tr>
            <tr><td><code>我的订阅</code></td><td>看进度；选一个后 <code>a</code> 立即搜索、<code>z</code> 暂停 / 恢复、<code>x</code> 取消订阅</td></tr>
            <tr><td><code>整理</code> / <code>同步</code></td><td>手动触发整理、增量同步（进任务队列）</td></tr>
            <tr><td><code>状态</code></td><td>当前任务、转存目录还有几个条目、最近几个任务的结果</td></tr>
            <tr><td><code>多份 编号 选择</code></td><td>同一集有几份、改名会重名时，选保留哪份（通知里会写怎么回）</td></tr>
            <tr><td><code>洗版 编号 是/否</code></td><td>手动转存的不如库里的、停下来问的：强制洗版还是不洗版</td></tr>
            <tr><td><code>帮助</code></td><td>列出以上指令</td></tr>
          </tbody>
        </table>
        <p class="note">找资源 / 订阅的会话 10 分钟内有效；关掉会话不会停已经提交的任务。</p>
      </section>

      <section class="block">
        <h3 class="block-title">常见问题</h3>
        <ul class="ul">
          <li><strong>测试报 60020</strong>：第 2 步的可信 IP 没填或填错了；服务器换过 IP 要跟着改</li>
          <li><strong>测试成功但手机上没收到</strong>：看应用的可见范围里有没有你；企业微信里找该应用的会话，不在「消息」列表就去「工作台」点开一次</li>
          <li><strong>企微保存接收消息时报「回调验证失败」</strong>：6086 端口没映射或没放行防火墙；Token / AESKey 与本页不一致；本页还没保存或没启用</li>
          <li><strong>发指令没反应</strong>：先确认第 3 步保存成功了，再看本站日志里有没有 <code>[企微</code> 开头的行</li>
        </ul>
      </section>
    </template>

    <!-- ==================== Telegram ==================== -->
    <template v-else-if="channel === 'tg'">
      <section class="block">
        <h3 class="block-title">能做什么</h3>
        <ul class="ul">
          <li><strong>通知推送</strong>：可以发给个人私聊、群或频道</li>
          <li><strong>私聊指令</strong>：找资源、订阅、发链接转存、整理 / 同步，搜索结果和待确认通知都带按钮，点一下就行</li>
        </ul>
        <p class="note">
          不需要公网地址、不用配回调：本站主动去 Telegram 拉消息（长轮询）。
          但服务器要能连上 <code>api.telegram.org</code>，国内部署请先在「系统配置 → 代理」配好全局代理。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">配置步骤</h3>
        <ul class="ul">
          <li>Telegram 里搜 <code>@BotFather</code>，发 <code>/newbot</code>，按提示起名字，最后拿到一串 <code>123456:ABC…</code> 的 Token</li>
          <li>Token 填到本页，状态选「启用」，先保存一次（Chat ID 暂时留空也行）</li>
          <li>在 Telegram 里打开你的机器人，点 Start，发 <code>/id</code>，机器人会回你的私聊 Chat ID</li>
          <li>把这个数字填进 Chat ID，再保存，点「测试通知」</li>
        </ul>
        <p class="note">本站启动机器人时会自动设好指令菜单（输入框旁的「/」按钮），也会清掉这个机器人原来设置的 Webhook。</p>
      </section>

      <section class="block">
        <h3 class="block-title">Chat ID 填谁</h3>
        <table class="tb">
          <thead><tr><th>填的是</th><th>通知</th><th>指令</th><th>说明</th></tr></thead>
          <tbody>
            <tr><td>你自己的私聊 ID（正数）</td><td>✓</td><td>✓</td><td>推荐。只有这一个私聊能操作机器人，别人私聊它不理</td></tr>
            <tr><td>群（负数）</td><td>✓</td><td>✗</td><td>先把机器人拉进群；群 ID 可以把群里一条消息转发给 <code>@userinfobot</code> 之类的机器人查</td></tr>
            <tr><td>频道（<code>-100</code> 开头）</td><td>✓</td><td>✗</td><td>要把机器人设为频道管理员</td></tr>
          </tbody>
        </table>
        <p class="note">待确认通知（同集多份、洗版待确认）的按钮只在填的是个人私聊时才带，群里只能回网页处理。</p>
      </section>

      <section class="block">
        <h3 class="block-title">私聊指令</h3>
        <table class="tb">
          <thead><tr><th>指令</th><th>做什么</th></tr></thead>
          <tbody>
            <tr><td><code>/search 片名</code></td><td>找资源：观影 / 盘搜 / TG 频道 / 不太灵 / RE0 一起搜</td></tr>
            <tr><td><code>/gy 片名</code> / <code>/wp 片名</code></td><td>只搜观影 / 只搜网盘</td></tr>
            <tr><td><code>/sub 片名</code></td><td>订阅（找资源的结果里也能点「订阅这部」）</td></tr>
            <tr><td><code>/subs</code></td><td>我的订阅：看进度、立即搜索、暂停、取消</td></tr>
            <tr><td><code>/download 链接</code></td><td>离线下载或 115 转存；也可以直接把链接和提取码发过来</td></tr>
            <tr><td><code>/organize</code> / <code>/sync</code></td><td>手动触发整理 / 增量同步</td></tr>
            <tr><td><code>/status</code></td><td>运行状态</td></tr>
            <tr><td><code>/dup</code> / <code>/wash</code></td><td>同集多份选哪份 / 洗版待确认（通知里的按钮也能选）</td></tr>
            <tr><td><code>/cancel</code></td><td>关闭当前搜索</td></tr>
            <tr><td><code>/id</code> / <code>/help</code></td><td>查看 Chat ID / 帮助</td></tr>
          </tbody>
        </table>
        <p class="note">
          不想打斜杠也行，直接发中文：「搜索 片名」「订阅 片名」「我的订阅」「整理」「同步」「状态」。
          搜索结果点按钮或回复序号；<code>0</code> 自动择优、<code>n</code> / <code>p</code> 翻页、<code>b</code> 重新选片、<code>r</code> 重搜、<code>q</code> 关闭。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">常见问题</h3>
        <ul class="ul">
          <li><strong>本页顶上的连接状态一直报错</strong>：多半是连不上 Telegram，检查全局代理</li>
          <li><strong>提示 401</strong>：Token 填错了，或者在 BotFather 里重置过 Token</li>
          <li><strong>提示 409 / 冲突</strong>：同一个 Token 还在别的程序里拉消息（另一个本站实例、MoviePilot 等），一个机器人只能给一个程序用</li>
          <li><strong>发到群失败（chat not found）</strong>：机器人还没进群，或者 ID 抄错了（群 ID 带负号）</li>
          <li><strong>重启后之前发的指令没执行</strong>：本站不补执行离线期间收到的指令，重新发一次</li>
        </ul>
      </section>
    </template>

    <!-- ==================== 飞书 ==================== -->
    <template v-else-if="channel === 'feishu'">
      <section class="block">
        <h3 class="block-title">能做什么</h3>
        <p class="p">只推送通知到一个飞书群，<strong>不支持指令</strong>。用的是群里的「自定义机器人」，不需要建飞书应用，也不需要公网地址。</p>
      </section>

      <section class="block">
        <h3 class="block-title">配置步骤</h3>
        <ul class="ul">
          <li>打开要收通知的飞书群（只有自己一个人的群也行）→ 右上角「设置」→「群机器人」→「添加机器人」→「自定义机器人」</li>
          <li>复制 Webhook 地址（<code>https://open.feishu.cn/open-apis/bot/v2/hook/…</code>），填到本页</li>
          <li>安全设置建议勾<strong>「签名校验」</strong>，把它给的密钥填进本页「签名密钥」</li>
          <li>状态选「启用」→ 保存配置 → 测试通知</li>
        </ul>
        <p class="note">Lark（国际版）一样用，Webhook 地址是 <code>open.larksuite.com</code> 开头的，直接填进来即可。</p>
      </section>

      <section class="block">
        <h3 class="block-title">安全设置怎么选</h3>
        <table class="tb">
          <thead><tr><th>选项</th><th>本站能不能用</th></tr></thead>
          <tbody>
            <tr><td>签名校验</td><td>推荐。密钥填到本页，本站每条消息都签名</td></tr>
            <tr><td>IP 白名单</td><td>可以，填本站服务器的出口公网 IP；IP 会变的话别选</td></tr>
            <tr><td>自定义关键词</td><td>不建议：本站的通知标题不固定，不含关键词的消息会被飞书拒收</td></tr>
          </tbody>
        </table>
      </section>

      <section class="block">
        <h3 class="block-title">常见问题</h3>
        <ul class="ul">
          <li><strong>报 19021（sign match fail）</strong>：签名密钥不对，或者服务器时间偏差太大（签名带时间戳，偏差超过一小时就失效）</li>
          <li><strong>报 19024（Key Words Not Found）</strong>：开了自定义关键词，换成签名校验</li>
          <li><strong>报 19022（Ip Not Allowed）</strong>：开了 IP 白名单而本站出口 IP 不在里面</li>
          <li><strong>开了签名校验却没填密钥</strong>：飞书会拒收，两边要一起设</li>
        </ul>
      </section>
    </template>

    <!-- ==================== QQ · OneBot ==================== -->
    <template v-else-if="channel === 'onebot'">
      <section class="block">
        <h3 class="block-title">能做什么</h3>
        <ul class="ul">
          <li><strong>通知推送</strong>：发到一个 QQ 群，或私聊发给某个 QQ</li>
          <li><strong>私聊指令</strong>（可选）：管理 QQ 私聊机器人，指令与企业微信那一套相同（搜索 / 订阅 / 发链接 / 整理 / 同步 / 状态 …），发「帮助」查看</li>
        </ul>
        <p class="note">
          OneBot 是第三方 QQ 机器人协议，本站不自带 QQ 客户端：你需要另外跑一个 NapCat / Lagrange / LLOneBot，
          用一个 <strong>QQ 小号</strong>登录当机器人（非官方客户端有被风控的可能，别用主号）。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">第 1 步：跑起 OneBot 实现（以 NapCat 为例）</h3>
        <ul class="ul">
          <li>按 NapCat 文档用 Docker 部署，打开它的 WebUI 扫码登录小号</li>
          <li>「网络配置」新建一个 <strong>HTTP 服务器</strong>，端口如 3000，设一个 Token（可不设）</li>
          <li>和本站在同一个 docker compose 里时，本页「HTTP 地址」写容器名，如 <code>http://napcat:3000</code>；不在一起就写宿主机 IP 加映射的端口</li>
          <li>Token 填到本页「Access Token」，没设就留空</li>
        </ul>
      </section>

      <section class="block">
        <h3 class="block-title">第 2 步：通知发给谁</h3>
        <ul class="ul">
          <li>「消息目标类型」选群聊就填群号（机器人小号要先在群里），选私聊就填接收的 QQ 号（建议先互加好友，陌生人私聊常被拦）</li>
          <li>状态选「启用」→ 保存配置 → 测试通知。到这里通知就能用了</li>
        </ul>
      </section>

      <section class="block">
        <h3 class="block-title">第 3 步：私聊指令（可选）</h3>
        <ul class="ul">
          <li>「管理 QQ」填你自己的 QQ 号。<strong>留空就不开指令</strong>，别人私聊机器人也不会触发任何操作</li>
          <li>「事件回调 Token」本页已经自动生成，保存后下面的回调地址就是带着它的完整地址</li>
          <li>
            在 NapCat「网络配置」再新建一个 <strong>HTTP 客户端</strong>（也叫 HTTP POST 上报），URL 填这个回调地址：
            <code>{{ origin }}/onebot/event?token=…</code>
          </li>
          <li>
            回调地址里的主机名是你现在打开本页用的地址。NapCat 不一定能访问到它（比如你用域名访问、NapCat 在内网），
            同一个 compose 里的话把主机部分换成本站的容器名加 6060 端口，如 <code>http://115-station:6060/onebot/event?token=…</code>
          </li>
          <li>用管理 QQ 私聊机器人发「帮助」，能收到指令列表就通了</li>
        </ul>
        <p class="note">
          指令的回复按通知发出：会同时发到所有已启用通道的通知目标（比如本页填的是群，回复也会出现在群里）。
          只认私聊里管理 QQ 发的消息，群消息一律不理。
        </p>
      </section>

      <section class="block">
        <h3 class="block-title">常见问题</h3>
        <ul class="ul">
          <li><strong>测试报连接失败</strong>：HTTP 地址不通，检查容器名 / 端口，以及 NapCat 的 HTTP 服务器有没有启用</li>
          <li><strong>报 HTTP 401 / 403</strong>：Access Token 与 NapCat 那边不一致</li>
          <li><strong>retcode 非 0</strong>：多半是目标不对——群号写错、小号不在群里、或私聊对象不是好友</li>
          <li><strong>发指令没反应</strong>：管理 QQ 没填或填错；NapCat 的 HTTP 客户端 URL 访问不到本站；回调 Token 改过之后没同步到 NapCat</li>
        </ul>
      </section>
    </template>

    <!-- ==================== QQ 官方机器人 ==================== -->
    <template v-else>
      <section class="block">
        <h3 class="block-title">先说限制</h3>
        <p class="p">
          QQ 开放平台对机器人<strong>主动</strong>往群里发消息管得很严，额度很少、规则还在收紧；
          本站只会发主动消息（不接收官方的事件推送，也就没法「回复」），所以经常会被平台拒发。
          只推荐已经有官方机器人、想顺手用一下的情况；要稳定收 QQ 通知，用 OneBot。
        </p>
        <p class="note">只支持推送通知到一个群，不支持指令。</p>
      </section>

      <section class="block">
        <h3 class="block-title">配置步骤</h3>
        <ul class="ul">
          <li>在 <code>q.qq.com</code> 注册开发者、创建机器人</li>
          <li>「开发设置」里拿到 <strong>AppID</strong> 和 <strong>AppSecret</strong>，填到本页</li>
          <li>同一页的「IP 白名单」加上本站服务器的出口公网 IP，否则换不到 access_token</li>
          <li>「沙箱配置」把要收通知的群加进去（没上线的机器人只能在沙箱群里发），再把机器人拉进群</li>
          <li>
            「群 ID」要的是平台给的 <code>group_openid</code>，不是群号。它只出现在官方推给机器人的事件里
            （有人在群里 @ 机器人时），本站不接收这些事件，需要借官方的事件调试工具或你自己的机器人程序查到
          </li>
          <li>状态选「启用」→ 保存配置 → 测试通知</li>
        </ul>
      </section>

      <section class="block">
        <h3 class="block-title">常见问题</h3>
        <ul class="ul">
          <li><strong>换取 token 失败</strong>：AppID / AppSecret 不对，或 IP 白名单里没有本站的出口 IP</li>
          <li><strong>HTTP 4xx，提示没有权限 / 超出频率</strong>：主动消息额度用完或平台不允许，换别的通道</li>
          <li><strong>群 ID 填了群号</strong>：要填 group_openid（一长串字母数字）</li>
        </ul>
      </section>
    </template>

    <section class="block">
      <h3 class="block-title">几个通道一起用</h3>
      <p class="p">
        五个通道可以同时开，每条通知会发到所有已启用的通道；「测试通知」也会对每个已启用的通道各发一条，
        哪个失败会在结果里写出来。保存配置一次保存全部通道，在哪个页签点都一样。
      </p>
      <p class="note">「测试通知」用的是<strong>已保存</strong>的配置：改完先点保存再测，否则测的还是旧值。</p>
    </section>
  </HelpDoc>
</template>
