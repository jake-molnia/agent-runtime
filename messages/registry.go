package messages

func (service *Service) HasStage(actor Actor) bool {
	_, exists := service.stages[actor]
	return exists
}
