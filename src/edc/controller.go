package edc

import (
	"encoding/json"
	"fmt"
	"github.com/vmihailenco/redis"
	"github.com/fitstar/falcore"
	"sort"
)

type Controller struct {
	client *redis.Client
}

func NewController(host string, port int) (*Controller, error) {
	client := redis.NewTCPClient(fmt.Sprintf("%v:%v", host, port), "", -1)
	return &Controller{
		client: client,
	}, nil
}

func (s *Controller) Set(key string, val interface{}) error {
	if j, err := json.Marshal(val); err == nil {
		sr := s.client.Set(key, string(j))
		return sr.Err()
	} else {
		return err
	}
	return nil
}

func (s *Controller) Get(key string, val interface{}) error {
	sr := s.client.Get(key)
	if sr.Err() != nil {
		return sr.Err()
	}
	return json.Unmarshal([]byte(sr.Val()), val)
}

func (s *Controller) Close() (error) {
	return s.client.Close()
}

func (s *Controller) VoteQuestion(question string) (error) {
	sr := s.client.ZIncrBy("questions/current", 1.0, question)
	return sr.Err()
}

func (s *Controller) NewConnectionHandler(ac *ActiveClient) {
	sp := s.GetQuestions()
	ac.pushChan <- sp
}

func (s *Controller) GetQuestions() (*ServerPush) {
	resp := s.client.ZRangeByScoreWithScoresMap("questions/current", "-inf", "+inf", 0, 1000)
	if resp.Err() != nil {
		falcore.Error("Zrange err: %v", resp.Err())
	}
	mp := resp.Val()
	falcore.Debug("qs: %#v", mp)
	questions := make(ByScore, len(mp))
	i := 0
	for q,s := range mp {
		questions[i] = &RQuestion{q,s}
		i++
	}
	sort.Sort(questions)
	data := make(map[string]interface{})
	data["questions"] = questions
	return &ServerPush{
		Method: "questions",
		Data: data,
	}
}


type RQuestion struct {
	Question string `json:"question"`
	Votes float64	`json:"votes"`
}
type ByScore []*RQuestion
func (a ByScore) Len() int           { return len(a) }
func (a ByScore) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByScore) Less(i, j int) bool { return a[j].Votes < a[i].Votes }
