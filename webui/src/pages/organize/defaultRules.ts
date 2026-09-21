/**
 * 首次使用时填入编辑器的默认规则（与旧前端 index.html 里内嵌的一致）。
 * 仅作起始模板。洗版的默认策略不在这里：后端首次部署就把它播种进库了
 * （model.DefaultWashYAML），前端再留一份就会出现「界面上有、库里没有」。
 */
export const DEFAULT_CATEGORY_YAML = "# 配置电影的分类策略\n# 分类名即 115 目录名（支持多级，用 / 分隔）\n# 整理后路径：{已存在目录}/{分类名}/{重命名文件}\nmovie:\n  # 如已存在目录为「影视库」，整理结果为：影视库/电影/动画电影/xxx\n  电影/动画电影:\n    # 匹配 genre_ids 内容类型，16是动漫\n    genre_ids: '16'\n  电影/华语电影:\n    # 匹配语种\n    original_language: 'zh,cn,bo,za'\n  # 未匹配以上条件时，返回最后一个\n  电影/外语电影:\n\n# 配置电视剧的分类策略\ntv:\n  电视剧/国漫:\n    genre_ids: '16'\n    origin_country: 'CN,TW,HK'\n  电视剧/日番:\n    genre_ids: '16'\n    origin_country: 'JP'\n  电视剧/国产剧:\n    origin_country: 'CN,TW,HK'\n  电视剧/欧美剧:\n    origin_country: 'US,FR,GB,DE,ES,IT,NL,PT,RU'\n  电视剧/日韩剧:\n    origin_country: 'JP,KP,KR,TH,IN,SG'\n  # 未匹配以上分类，则命名为未分类\n  电视剧/未分类:\n\n"
