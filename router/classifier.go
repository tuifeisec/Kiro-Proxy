package router

const ClassifierDisabledSetting = "__disabled__"

func IsClassifierDisabledSetting(value string) bool {
	return value == ClassifierDisabledSetting
}
