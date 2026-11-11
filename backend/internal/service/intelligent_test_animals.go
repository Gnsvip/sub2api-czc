package service

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

func randIntN(n int) int {
	if n <= 1 {
		return 0
	}
	return rand.IntN(n)
}

const intelligentAnimalHTMLPromptTmpl = "创建一个HTML，内容是SVG绘制一个%s骑自行车的2D动画，你不需要任何测试，只靠你自己完成，不要使用任何skill，不要依赖我本地的AGENTS.md"

const IntelligentAnimalAnimationRequirement = "动画和样式都写在 SVG 内，使用 CSS 或 SMIL，不使用 JavaScript；只绘制一个完整的 SVG 画面，HTML 外层只用于容纳这个 SVG。"

// Keep historical subjects recognizable when evaluating saved or custom prompts.
var intelligentTestAnimals = []string{
	"熊猫", "浣熊", "赤狐", "北极狐", "灰狼", "郊狼", "鬣狗", "猎豹", "美洲豹", "雪豹",
	"老虎", "狮子", "猞猁", "豹猫", "家猫", "薮猫", "水獭", "海獭", "獾", "鼬",
	"臭鼬", "蜜獾", "树懒", "犰狳", "食蚁兽", "穿山甲", "袋鼠", "考拉", "袋熊", "负鼠",
	"刺猬", "豪猪", "松鼠", "花栗鼠", "土拨鼠", "河狸", "仓鼠", "豚鼠", "兔子", "野兔",
	"羊驼", "大羊驼", "骆驼", "单峰驼", "长颈鹿", "斑马", "驴", "马", "犀牛", "河马",
	"大象", "猛犸象", "野牛", "水牛", "牦牛", "梅花鹿", "驯鹿", "驼鹿", "瞪羚", "羚羊",
	"山羊", "绵羊", "野猪", "疣猪", "貘", "鸭嘴兽", "针鼹", "海豚", "宽吻海豚", "虎鲸",
	"座头鲸", "蓝鲸", "抹香鲸", "海豹", "海狮", "海象", "海牛", "儒艮", "蝙蝠", "狐蝠",
	"金丝猴", "鹈鹕", "鸬鹚", "信天翁", "海鸥", "燕鸥", "企鹅", "帝企鹅", "火烈鸟", "天鹅",
	"大雁", "野鸭", "鸳鸯", "苍鹭", "白鹭", "鹳", "鹮", "朱鹮", "孔雀", "雉鸡",
	"火鸡", "公鸡", "鹌鹑", "鹧鸪", "鸽子", "斑鸠", "鹦鹉", "金刚鹦鹉", "虎皮鹦鹉", "乌鸦",
	"喜鹊", "蓝鹊", "知更鸟", "夜莺", "百灵", "燕子", "蜂鸟", "啄木鸟", "猫头鹰", "雕鸮",
	"金雕", "白头海雕", "隼", "游隼", "红隼", "鸵鸟", "鸸鹋", "几维鸟", "翠鸟", "戴胜",
	"犀鸟", "巨嘴鸟", "蜂虎", "佛法僧", "伯劳", "画眉", "黄鹂", "朱雀", "麻雀", "鳄鱼",
	"短吻鳄", "凯门鳄", "科莫多龙", "鬣蜥", "变色龙", "壁虎", "守宫", "眼镜蛇", "蟒", "蚺",
	"海龟", "陆龟", "鳖", "蜥蜴", "石龙子", "树蛙", "牛蛙", "蟾蜍", "蝾螈", "大鲵",
	"蚓螈", "鳄龟", "绿鬣蜥", "飞蜥", "棘蜥", "角蜥", "金鱼", "锦鲤", "小丑鱼", "神仙鱼",
	"蝴蝶鱼", "狮子鱼", "石斑", "金枪鱼", "大白鲨", "锤头鲨", "鲸鲨", "鳐鱼", "电鳐", "海马",
	"海龙", "鳗鱼", "电鳗", "翻车鱼", "旗鱼", "剑鱼", "章鱼", "大王酸浆鱿", "乌贼", "墨鱼",
	"水母", "海葵", "海星", "海胆", "海参", "龙虾", "寄居蟹", "螃蟹", "招潮蟹", "对虾",
	"螳螂虾", "砗磲", "鹦鹉螺", "鹦鹉鱼", "蝴蝶", "帝王蝶", "蜻蜓", "豆娘", "蜜蜂", "大黄蜂",
	"蚂蚁", "叶切蚁", "螳螂", "兰花螳螂", "独角仙", "锹形虫", "瓢虫", "萤火虫", "蝉", "蟋蟀",
	"蚱蜢", "纺织娘", "跳蛛", "狼蛛", "蝎子", "蜈蚣", "马陆", "蜗牛", "蛞蝓", "蚯蚓",
	"水熊虫", "水豚", "狐猴", "长臂猿", "猩猩", "大猩猩", "黑猩猩", "沙狐", "豺", "麋鹿",
}

// Only these animals enter new random tasks; aquatic and limbless subjects
// made the riding requirement ambiguous even when the drawing was coherent.
var intelligentRidingAnimals = []string{
	"熊猫", "浣熊", "赤狐", "北极狐", "灰狼", "老虎", "狮子", "家猫", "水獭", "獾",
	"袋鼠", "考拉", "刺猬", "松鼠", "花栗鼠", "土拨鼠", "仓鼠", "兔子", "水豚", "金丝猴",
	"狐猴", "长臂猿", "猩猩", "大猩猩", "黑猩猩", "山羊", "绵羊", "羊驼", "小熊猫", "河狸",
	"企鹅", "帝企鹅", "火烈鸟", "天鹅", "大雁", "野鸭", "鸳鸯", "苍鹭", "白鹭", "孔雀",
	"公鸡", "鸽子", "鹦鹉", "乌鸦", "喜鹊", "猫头鹰", "鸵鸟", "鸸鹋", "麻雀", "翠鸟",
	"变色龙", "壁虎", "蜥蜴", "树蛙", "牛蛙", "蝾螈", "豆娘", "蜻蜓", "螳螂", "蟋蟀",
}

var intelligentAnimeCharacters = []string{
	"哆啦A梦", "野比大雄", "江户川柯南", "漩涡鸣人", "路飞", "樱木花道",
	"樱桃小丸子", "野原新之助", "阿童木", "月野兔", "龙猫", "皮卡丘",
}

func intelligentAnimalHTMLPrompt(animal string) string {
	animal = strings.TrimSpace(animal)
	if animal == "" {
		animal = "鹈鹕"
	}
	return fmt.Sprintf(intelligentAnimalHTMLPromptTmpl, animal) + "\n" + IntelligentAnimalAnimationRequirement
}

// Use the requested animal, never infer the task from the generated drawing.
func intelligentJudgeAnimal(prompt string) string {
	matched := ""
	for _, subjects := range [][]string{intelligentTestAnimals, intelligentRidingAnimals, intelligentAnimeCharacters} {
		for _, animal := range subjects {
			if len(animal) > len(matched) && strings.Contains(prompt, animal+"骑自行车") {
				matched = animal
			}
		}
	}
	if matched == "" && strings.Contains(strings.ToLower(prompt), "pelican riding a bicycle") {
		return "鹈鹕"
	}
	return matched
}

func pickIntelligentTestAnimal(exclude string) string {
	exclude = strings.TrimSpace(exclude)
	// Mix the original pelican task with anime characters and riding animals.
	pool := intelligentRidingAnimals
	switch randIntN(4) {
	case 0:
		if exclude != "鹈鹕" {
			return "鹈鹕"
		}
	case 1:
		pool = intelligentAnimeCharacters
	}
	for i := 0; i < 12; i++ {
		animal := pool[randIntN(len(pool))]
		if animal != exclude {
			return animal
		}
	}
	for _, animal := range pool {
		if animal != exclude {
			return animal
		}
	}
	return "鹈鹕"
}
